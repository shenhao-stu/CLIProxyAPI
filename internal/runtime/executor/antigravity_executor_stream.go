package executor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// ExecuteStream performs a streaming request to the Antigravity API.
func (e *AntigravityExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /responses/compact"}
	}
	if helps.HasResponsesCompactionItem(req.Payload) {
		expanded, errExpand := helps.ExpandAntigravityCompactionCapsules(req.Payload)
		if errExpand != nil {
			return nil, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
		}
		req.Payload = expanded
		if len(opts.OriginalRequest) > 0 {
			expandedOrig, errOrig := helps.ExpandAntigravityCompactionCapsules(opts.OriginalRequest)
			if errOrig == nil {
				opts.OriginalRequest = expandedOrig
			} else {
				opts.OriginalRequest = expanded
			}
		}
	}
	if helps.HasResponsesCompactionTrigger(req.Payload) || helps.HasResponsesCompactionTrigger(opts.OriginalRequest) {
		return e.executeCompactionStream(ctx, auth, req, opts)
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	ctx = context.WithValue(ctx, "alt", "")
	if !antigravityCoolingDisabled(auth, e.cfg) {
		if inCooldown, remaining, errCooldown := antigravityIsInShortCooldownRequired(ctx, auth, baseModel, time.Now()); errCooldown != nil {
			return nil, homeKVUnavailableStatusErr(errCooldown)
		} else if inCooldown && !antigravityShouldBypassShortCooldown(ctx, e.cfg) {
			log.Debugf("antigravity executor: auth %s in short cooldown for model %s (%s remaining), returning 429 to switch auth", auth.ID, baseModel, remaining)
			d := remaining
			return nil, statusErr{code: http.StatusTooManyRequests, msg: fmt.Sprintf("auth in short cooldown, %s remaining", remaining), retryAfter: &d}
		}
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("antigravity")

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalPayload, errValidate := validateAntigravityRequestSignatures(ctx, baseModel, from, originalPayload)
	if errValidate != nil {
		return nil, errValidate
	}
	req.Payload = originalPayload
	// Observe a translation refusal before the token refresh or any upstream call.
	modelInfo, _ := cliproxyauth.ResolvedModelInfo(req)
	translationReq := sdktranslator.RequestEnvelope{Format: from, Model: baseModel, Stream: true, ModelInfo: modelInfo}
	originalTranslated, translated, errTranslate := helps.TranslateRequestEnvelopePairWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, translationReq, originalPayload, req.Payload)
	if errTranslate != nil {
		return nil, errTranslate
	}
	token, updatedAuth, errToken := e.ensureAccessToken(ctx, auth)
	if errToken != nil {
		return nil, errToken
	}
	if updatedAuth != nil {
		auth = updatedAuth
		reporter.UpdateAccessTokenFingerprint(auth)
	}

	doc := ojson.NewDoc(translated)
	if err = helps.ApplyRequestThinkingDoc(doc, req, opts, from.String(), to.String(), e.Identifier()); err != nil {
		return nil, err
	}

	finalize := helps.NewPayloadFinalizer(e.cfg, e.Identifier(), baseModel, "antigravity", "request", originalTranslated, req, opts)
	ctx = helps.WithPayloadFinalizer(ctx, finalize)
	e.obfuscateSensitiveWordsDoc(doc)
	sanitizeAntigravityGeminiRequestSignaturesDoc(baseModel, doc)
	_ = doc.Delete("request.stream")

	useCredits := cliproxyauth.AntigravityCreditsRequested(ctx) && antigravityCreditsRetryEnabled(e.cfg)

	baseURL := resolveAntigravityRequestBaseURL(auth)
	httpClient := newAntigravityHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)

	// Credential retry rounds are owned by the conductor. Perform one upstream
	// request per credential so request-retry is not consumed twice.
	if useCredits {
		if injectEnabledCreditTypesDoc(doc) {
			helps.MarkCreditsUsed(ctx)
		}
	}
	replayScope := antigravityReasoningReplayScope{}
	if antigravityUsesReasoningReplayCache(baseModel) {
		var errReplay error
		replayScope, _, errReplay = prepareAntigravityGeminiReasoningReplayPayloadDoc(ctx, baseModel, req, opts, doc)
		if errReplay != nil {
			err = errReplay
			return nil, err
		}
	}
	ensureAntigravityGeminiBoundaryUserContentDoc(baseModel, doc)
	var replayRequestPayload []byte
	if replayScope.valid() {
		replayRequestPayload, _ = antigravityReplayRequestEnvelopeFromDoc(doc)
	}
	httpReq, requestPayload, errReq := e.buildRequestDoc(ctx, auth, token, baseModel, doc, true, opts.Alt, baseURL, helps.DerivedAntigravitySessionID(opts.Metadata, req.Metadata))
	if errReq != nil {
		err = errReq
		return nil, err
	}
	translated = requestPayload
	reporter.SetTranslatedRequestMetadata(requestPayload, to.String())
	trace := helps.NewArgonStreamTrace(baseModel, responseFormat, time.Now())
	argonClaude := util.IsGeminiArgon(baseModel) && responseFormat == sdktranslator.FormatClaude
	httpResp, errDo := helps.WithAntigravityHTTPClientTrace(httpClient, auth, "generate_stream").Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		if errors.Is(errDo, context.Canceled) || errors.Is(errDo, context.DeadlineExceeded) {
			return nil, errDo
		}
		err = errDo
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close response body error: %v", errClose)
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, bodyBytes)
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			if errors.Is(errRead, context.Canceled) || errors.Is(errRead, context.DeadlineExceeded) {
				err = errRead
				return nil, err
			}
			if errCtx := ctx.Err(); errCtx != nil {
				err = errCtx
				return nil, err
			}
			err = errRead
			return nil, err
		}
		if httpResp.StatusCode == http.StatusTooManyRequests {
			decision := decideAntigravity429(bodyBytes)

			switch decision.kind {
			case antigravity429DecisionShortCooldownSwitchAuth:
				closeAntigravityAuthIdleTransports(auth)
				if decision.retryAfter != nil && *decision.retryAfter > 0 && !antigravityCoolingDisabled(auth, e.cfg) {
					if errMarkCooldown := markAntigravityShortCooldownRequired(ctx, auth, baseModel, time.Now(), *decision.retryAfter); errMarkCooldown != nil {
						err = homeKVUnavailableStatusErr(errMarkCooldown)
						return nil, err
					}
					log.Debugf("antigravity executor: short quota cooldown (%s) for model %s recorded", *decision.retryAfter, baseModel)
				}
			case antigravity429DecisionFullQuotaExhausted:
				closeAntigravityAuthIdleTransports(auth)
				if useCredits && antigravityHasExplicitCreditsBalanceExhaustedReason(bodyBytes) && !antigravityCoolingDisabled(auth, e.cfg) {
					markAntigravityCreditsPermanentlyDisabled(auth)
				}
				// No credits logic - just fall through to error return below
			}
		}

		if errClear := clearAntigravityReasoningReplayOnInvalidSignature(ctx, replayScope, httpResp.StatusCode, bodyBytes); errClear != nil {
			// Report the upstream failure rather than the cleanup failure.
			logAntigravityReasoningReplayDegraded(replayScope, "invalidate", errClear)
		}
		err = e.newStatusErr(auth, baseModel, httpResp.StatusCode, httpResp.Header, bodyBytes)
		return nil, err
	}

	// Stream success
	if useCredits {
		clearAntigravityCreditsFailureState(auth)
	}
	if len(replayRequestPayload) == 0 {
		replayRequestPayload = requestPayload
	}
	replayAccumulator := newAntigravityReasoningReplayAccumulator(replayScope, replayRequestPayload)
	out := make(chan cliproxyexecutor.StreamChunk)
	stopDeliverySupport := usage.SupportStreamDelivery(ctx)
	go func(resp *http.Response) {
		defer func() {
			if errClose := resp.Body.Close(); errClose != nil {
				log.Errorf("antigravity executor: close response line error: %v", errClose)
			}
		}()
		idleTimeout := antigravityCarbonReadTimeout(auth, baseModel, responseFormat)
		thoughtWindowEnds := time.Now().Add(antigravityCarbonThoughtWindow)
		body := &antigravityIdleStream{ReadCloser: resp.Body}
		scanner := bufio.NewScanner(body)
		scanner.Buffer(nil, streamScannerBuffer)
		claudeInputTokens := helps.NewClaudeInputTokenState(from, to, responseFormat, originalPayload)
		var streamUsage helps.StreamUsageBuffer
		streamUsage.SetRequestPayload(requestPayload)
		var streamErr error
		var pendingJSON []byte
		var replayCommitted bool
		var terminalDelivered bool
		var param any
		defer trace.Log(ctx)
		progress := helps.NewGeminiResponsesProgress(baseModel, responseFormat)
		defer func() {
			outcomeErr := streamErr
			if outcomeErr == nil {
				outcomeErr = scanner.Err()
			}
			progress.LogOutcome(ctx, outcomeErr)
		}()
		commitReplay := func() {
			if !replayCommitted && replayAccumulator != nil && replayAccumulator.terminal && helps.ApplyPatchTranslationError(param) == nil {
				replayAccumulator.Commit(ctx)
				replayCommitted = true
			}
		}
		defer func() {
			defer stopDeliverySupport()
			if helps.ApplyPatchTranslationError(param) != nil {
				streamErr = statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
				stopDeliverySupport()
			}
			// Replay is settled before forwarding completion; accounting still waits
			// for the HTTP consumer, which may need EOF to acknowledge delivery.
			if terminalDelivered {
				commitReplay()
			}
			close(out)
			if streamErr == nil {
				streamErr = ctx.Err()
			}
			if deliveryErr, tracked := usage.WaitStreamDelivery(ctx); tracked {
				if streamErr == nil || errors.Is(streamErr, context.Canceled) {
					streamErr = deliveryErr
				}
			}
			if streamErr != nil {
				if terminalDelivered && errors.Is(streamErr, context.Canceled) {
					streamUsage.Publish(ctx, reporter)
					reporter.EnsurePublished(ctx)
					return
				}
				if !streamUsage.PublishFailure(ctx, reporter, streamErr) {
					reporter.PublishFailure(ctx, streamErr)
				}
				return
			}

			streamUsage.Publish(ctx, reporter)
			reporter.EnsurePublished(ctx)
		}()
		var pendingFilterPayload []byte
		var pendingFilterMsg string
		helps.InitializeApplyPatchStream(ctx, to, responseFormat, req.Model, helps.ApplyPatchOriginalRequest(req, opts), translated, &param)
		protocolDone := false
		for {
			if body.timeout > 0 {
				body.timeout = antigravityCarbonReadBudget(idleTimeout,
					len(pendingFilterPayload) == 0 && progress.ThinkingOnly(), time.Until(thoughtWindowEnds))
			}
			if !scanner.Scan() {
				break
			}
			line := scanner.Bytes()
			// The explicit SSE terminator ends a turn independently of the HTTP socket.
			frame := bytes.TrimSpace(line)
			if data, ok := bytes.CutPrefix(frame, []byte("data:")); ok {
				frame = bytes.TrimSpace(data)
			}
			if argonClaude && bytes.Equal(frame, []byte("[DONE]")) {
				if len(pendingJSON) != 0 {
					streamErr = statusErr{code: http.StatusBadGateway, msg: "Upstream ended inside an incomplete stream frame"}
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
					case <-ctx.Done():
					}
					return
				}
				protocolDone = true
				break
			}
			rawLine := line
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if replayAccumulator != nil {
				replayAccumulator.ObserveSSELine(line)
			}

			// Capture accounting before the client-facing filter renames usage.
			streamUsage.ObserveAntigravityStream(line)

			payload := helps.JSONPayload(line)
			if len(payload) > 0 {
				trace.Native()
			}
			if len(pendingJSON) > 0 {
				trimmedLine := bytes.TrimSpace(line)
				if bytes.HasPrefix(trimmedLine, []byte("data:")) {
					trimmedLine = bytes.TrimSpace(trimmedLine[len("data:"):])
				}
				if len(trimmedLine) > 0 {
					pendingJSON = append(pendingJSON, '\n')
					pendingJSON = append(pendingJSON, trimmedLine...)
				}
				if !ojson.ValidBytes(pendingJSON) {
					continue
				}
				payload = pendingJSON
				rawLine = payload
				pendingJSON = nil
			} else if payload != nil && !ojson.ValidBytes(payload) {
				pendingJSON = append([]byte(nil), payload...)
				continue
			}
			if payload == nil {
				for _, chunk := range progress.Forward(rawLine, nil) {
					select {
					case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
						progress.Forwarded(chunk)
					case <-ctx.Done():
						return
					}
				}
				continue
			}
			if errorResult := ojson.GetBytesNoCopy(payload, "error"); errorResult.Exists() {
				progress.Forward(rawLine, nil)
				statusCode := int(errorResult.Get("code").Int())
				if statusCode < http.StatusBadRequest || statusCode > 599 {
					statusCode = http.StatusBadGateway
				}
				streamErr = e.newStatusErr(auth, baseModel, statusCode, resp.Header, payload)
				stopDeliverySupport()
				helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
				case <-ctx.Done():
				}
				return
			}
			processPayload := func(source []byte) bool {
				// Filter usage metadata for all models after block classification
				// so only the terminal chunk retains client-visible usage statistics.
				p := helps.JSONPayload(helps.FilterSSEUsageMetadata(append([]byte("data: "), source...)))
				if len(p) == 0 {
					return true
				}
				reporter.ObserveResponseModel(p)

				p = e.resolveWebSearchGroundingURLs(ctx, auth, from, originalPayload, translated, p)
				chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, helps.ApplyPatchOriginalRequest(req, opts), translated, bytes.Clone(p), &param, claudeInputTokens)
				helps.RecordApplyPatchStreamFailure(ctx, param, reporter, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
				if helps.ApplyPatchTranslationError(param) == nil {
					chunks = progress.Forward(source, chunks)
				}
				var isTerminalChunk bool
				if argonClaude {
					for _, chunk := range chunks {
						if helps.IsGeminiTerminalStreamChunk(chunk) {
							isTerminalChunk = true
							break
						}
					}
					helps.CaptureTranslatedClaudeStreamError(responseFormat, chunks, &streamErr, func(msg string) error { return statusErr{code: http.StatusBadGateway, msg: msg} })
				} else {
					if finishReason := ojson.GetBytesNoCopy(p, "candidates.0.finishReason"); finishReason.Exists() && strings.TrimSpace(finishReason.String()) != "" {
						isTerminalChunk = true
					} else if finishReason := ojson.GetBytesNoCopy(p, "response.candidates.0.finishReason"); finishReason.Exists() && strings.TrimSpace(finishReason.String()) != "" {
						isTerminalChunk = true
					} else if replayAccumulator != nil && replayAccumulator.terminal {
						isTerminalChunk = true
					}
					if !isTerminalChunk {
						for _, chunk := range chunks {
							for _, cLine := range bytes.Split(chunk, []byte("\n")) {
								trimmed := bytes.TrimSpace(cLine)
								if bytes.Equal(trimmed, []byte("data: [DONE]")) || bytes.Equal(trimmed, []byte("[DONE]")) {
									isTerminalChunk = true
									break
								}
								cPayload := helps.JSONPayload(cLine)
								if len(cPayload) == 0 {
									continue
								}
								cType := ojson.GetBytesNoCopy(cPayload, "type").String()
								if cType == "response.completed" || cType == "message_stop" {
									isTerminalChunk = true
									break
								}
								if finishReason := ojson.GetBytesNoCopy(cPayload, "choices.0.finish_reason"); finishReason.Exists() && strings.TrimSpace(finishReason.String()) != "" {
									isTerminalChunk = true
									break
								}
							}
							if isTerminalChunk {
								break
							}
						}
					}
				}
				if argonClaude && isTerminalChunk && streamErr == nil {
					commitReplay()
				}
				if replayAccumulator != nil && replayAccumulator.terminal && !replayCommitted && antigravityStreamChunksHaveTerminalCommitEvent(chunks) {
					commitReplay()
				}
				for i := range chunks {
					select {
					case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
						progress.Forwarded(chunks[i])
						trace.Forwarded(chunks[i])
					case <-ctx.Done():
						return false
					}
				}
				if len(chunks) > 0 {
					body.timeout = idleTimeout
				}
				if isTerminalChunk {
					terminalDelivered = true
				}
				if argonClaude && (isTerminalChunk || streamErr != nil) {
					return false
				}
				return !helps.StopApplyPatchStream(ctx, param, reporter, out, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
			}
			if len(pendingFilterPayload) > 0 {
				flushed := pendingFilterPayload
				pendingFilterPayload = nil
				pendingFilterMsg = ""
				if !processPayload(flushed) {
					return
				}
			}
			if filterMsg, blocked := clienterror.GeminiFilterBlockMessage(payload); blocked {
				pendingFilterPayload = bytes.Clone(payload)
				pendingFilterMsg = filterMsg
				continue
			}
			if !processPayload(payload) {
				return
			}
		}
		if len(pendingJSON) != 0 && scanner.Err() == nil && ctx.Err() == nil {
			progress.IncompleteFrame(len(pendingJSON))
			streamErr = statusErr{code: http.StatusBadGateway, msg: "Upstream ended inside an incomplete stream frame"}
			stopDeliverySupport()
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
			case <-ctx.Done():
			}
			return
		}
		scanErr := scanner.Err()
		if pendingFilterMsg != "" && !terminalDelivered &&
			(scanErr == nil || errors.Is(scanErr, errAntigravityStreamIdle) && ctx.Err() == nil) {
			progress.FilterRejected(http.StatusBadRequest)
			streamErr = statusErr{code: http.StatusBadRequest, msg: pendingFilterMsg}
			stopDeliverySupport()
			helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
			case <-ctx.Done():
			}
			return
		}
		if helps.EndApplyPatchStream(ctx, param, reporter, out, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}) {
			return
		}
		if errScan := scanErr; errScan != nil {
			if terminalDelivered && errors.Is(errScan, errAntigravityStreamIdle) {
				return
			}
			errScan = classifyAntigravityCarbonReadError(ctx, errScan)
			streamErr = errScan
			if errors.Is(errScan, context.Canceled) && ctx.Err() != nil {
				if terminalDelivered {
					return
				}
				if deliveryErr, tracked := usage.WaitStreamDelivery(ctx); tracked && deliveryErr == nil {
					return
				}
			}
			stopDeliverySupport()
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		} else if ctx.Err() == nil {
			// Only a clean end of stream may produce a synthetic terminal event.
			// Translating [DONE] after a read error would report a truncated
			// stream as a successful completion. A clean EOF is a normal end
			// even without finishReason: real Antigravity tool-call streams
			// end that way (see antigravityNoFinishReasonSSE).
			tail := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, helps.ApplyPatchOriginalRequest(req, opts), translated, []byte("[DONE]"), &param, claudeInputTokens)
			tail = progress.Forward(nil, tail)
			if argonClaude {
				helps.CaptureTranslatedClaudeStreamError(responseFormat, tail, &streamErr, func(msg string) error { return statusErr{code: http.StatusBadGateway, msg: msg} })
			}
			helps.RecordApplyPatchStreamFailure(ctx, param, reporter, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
			// Publish scoped replay before the client can send its tool result.
			// Explicit DONE plus a validated Claude terminal also proves a native
			// no-finish tool turn. A broken or unmetered turn cannot replace history.
			if streamErr == nil {
				if argonClaude && protocolDone && replayAccumulator != nil && len(tail) > 0 && helps.IsGeminiTerminalStreamChunk(tail[0]) {
					replayAccumulator.terminal = true
				}
				commitReplay()
			}
			for i := range tail {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: tail[i]}:
					trace.Forwarded(tail[i])
				case <-ctx.Done():
					return
				}
			}
			terminalDelivered = streamErr == nil
		}
	}(httpResp)
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *AntigravityExecutor) executeCompactionStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	payload := req.Payload
	if len(payload) == 0 && len(opts.OriginalRequest) > 0 {
		payload = opts.OriginalRequest
	}
	summaryPayload := helps.PrepareAntigravityCompactionSummaryPayload(payload, baseModel)

	summaryReq := cliproxyexecutor.Request{
		Model:    req.Model,
		Payload:  summaryPayload,
		Metadata: req.Metadata,
	}
	summaryOpts := opts
	summaryOpts.Alt = ""
	summaryOpts.Stream = false
	summaryOpts.OriginalRequest = nil
	summaryOpts.SourceFormat = sdktranslator.FormatOpenAIResponse
	summaryOpts.ResponseFormat = sdktranslator.FormatOpenAIResponse

	summaryResp, errSummary := e.Execute(ctx, auth, summaryReq, summaryOpts)
	if errSummary != nil {
		return nil, errSummary
	}

	summaryText, errExtract := helps.ExtractAntigravitySummaryText(summaryResp.Payload)
	if errExtract != nil {
		return nil, fmt.Errorf("extract summary: %w", errExtract)
	}
	capsule, errSeal := helps.SealAntigravityCompaction(summaryText, baseModel)
	if errSeal != nil {
		return nil, fmt.Errorf("seal compaction capsule: %w", errSeal)
	}

	inputTokens := int(ojson.GetBytesNoCopy(summaryResp.Payload, "usage.input_tokens").Int())
	outputTokens := int(ojson.GetBytesNoCopy(summaryResp.Payload, "usage.output_tokens").Int())
	totalTokens := int(ojson.GetBytesNoCopy(summaryResp.Payload, "usage.total_tokens").Int())
	if totalTokens == 0 && inputTokens == 0 {
		usage := helps.ParseOpenAIUsage(summaryResp.Payload)
		inputTokens = int(usage.InputTokens)
		outputTokens = int(usage.OutputTokens)
		totalTokens = int(usage.TotalTokens)
	}

	chunks := helps.BuildAntigravityCompactionStreamChunks(baseModel, capsule, inputTokens, outputTokens, totalTokens)
	out := make(chan cliproxyexecutor.StreamChunk, len(chunks))
	for _, chunk := range chunks {
		out <- cliproxyexecutor.StreamChunk{Payload: chunk}
	}
	close(out)

	headers := summaryResp.Headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Content-Type", "text/event-stream")

	return &cliproxyexecutor.StreamResult{
		Headers: headers,
		Chunks:  out,
	}, nil
}
