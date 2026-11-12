package common

import (
	"github.com/MoeForge/moego/ojson"
	"math"
	"strconv"
	"strings"
)

// Gemini usage frames are cumulative snapshots, not additive deltas.
type GeminiUsageSnapshot struct {
	Present    bool
	seen       uint8
	Complete   bool
	Prompt     int64
	Candidates int64
	Thoughts   int64
	Total      int64
	Cached     int64
}

func (usage *GeminiUsageSnapshot) Merge(root ojson.Result, terminal bool) bool {
	usage.Complete = false
	metadataName := "usageMetadata"
	metadata := root.Get(metadataName)
	if !metadata.Exists() {
		metadataName = "cpaUsageMetadata"
		metadata = root.Get(metadataName)
	}
	if !metadata.IsObject() || !ojson.Valid(metadata.Raw) {
		return false
	}
	metadataCount := 0
	root.ForEach(func(key, _ ojson.Result) bool {
		if key.String() == metadataName {
			metadataCount++
		}
		return metadataCount < 2
	})
	if metadataCount != 1 {
		return false
	}
	members := make(map[string]ojson.Result)
	unique := true
	metadata.ForEach(func(key, value ojson.Result) bool {
		name := key.String()
		if _, exists := members[name]; exists {
			unique = false
			return false
		}
		members[name] = value
		return true
	})
	if !unique {
		return false
	}
	next := *usage
	completeZero, positive := terminal, false
	frameSeen, validFrame := uint8(0), true
	for index, field := range []struct {
		name  string
		value *int64
	}{
		{"promptTokenCount", &next.Prompt},
		{"candidatesTokenCount", &next.Candidates},
		{"thoughtsTokenCount", &next.Thoughts},
		{"totalTokenCount", &next.Total},
		{"cachedContentTokenCount", &next.Cached},
	} {
		value := members[field.name]
		count, valid := geminiUsageCount(value)
		if valid {
			frameSeen |= 1 << index
		} else if value.Exists() {
			validFrame = false
		}
		if (index < 2 || value.Exists()) && (!valid || count != 0) {
			completeZero = false
		}
		if valid && count > 0 {
			next.seen |= 1 << index
			positive = true
			// A later placeholder or stale cumulative frame must not erase usage.
			if count > *field.value {
				*field.value = count
			}
		}
	}
	if next.Candidates > math.MaxInt64-next.Thoughts {
		return false
	}
	if terminal {
		if cached, valid := geminiTerminalCache(members, usage.Prompt); valid {
			next.Cached = cached
		}
	}
	observed := positive || completeZero
	next.Complete = terminal && validFrame && frameSeen&3 == 3
	if next.Complete {
		// A zero primer or separate partial frames cannot prove a final zero bucket.
		next.seen |= frameSeen
	}
	next.Present = next.Present || observed
	*usage = next
	return observed
}

func geminiUsageCount(value ojson.Result) (int64, bool) {
	if value.Type != ojson.Number {
		return 0, false
	}
	count, err := strconv.ParseInt(value.Raw, 10, 64)
	return count, err == nil && count >= 0
}

func GeminiHasSourceFinish(root ojson.Result, finish string) bool {
	if finish == "" {
		value := root.Get("candidates.0.finishReason")
		if value.Type != ojson.String {
			return false
		}
		finish = value.String()
	}
	finish = strings.TrimSpace(finish)
	return finish != "" && !strings.EqualFold(finish, "FINISH_REASON_UNSPECIFIED")
}

func geminiTerminalCache(members map[string]ojson.Result, previousPrompt int64) (int64, bool) {
	prompt, valid := geminiUsageCount(members["promptTokenCount"])
	if !valid || prompt <= 0 || prompt < previousPrompt {
		return 0, false
	}
	candidates, valid := geminiUsageCount(members["candidatesTokenCount"])
	if !valid {
		return 0, false
	}
	var thoughts int64
	if value := members["thoughtsTokenCount"]; value.Exists() {
		thoughts, valid = geminiUsageCount(value)
		if !valid {
			return 0, false
		}
	}
	total, valid := geminiUsageCount(members["totalTokenCount"])
	if !valid || candidates > math.MaxInt64-thoughts || prompt > math.MaxInt64-candidates-thoughts || total != prompt+candidates+thoughts {
		return 0, false
	}
	// A complete terminal snapshot can correct cache classification without
	// erasing input, output, or reasoning usage already observed in the stream.
	cached, valid := geminiUsageCount(members["cachedContentTokenCount"])
	return cached, valid && cached <= prompt
}

func (usage GeminiUsageSnapshot) ResponsesJSON() []byte {
	out := ojson.NewDocString(`{}`)
	for index, field := range []struct {
		path  string
		value int64
	}{
		{"input_tokens", usage.Prompt},
		{"output_tokens", usage.Candidates + usage.Thoughts},
		{"output_tokens_details.reasoning_tokens", usage.Thoughts},
		{"total_tokens", usage.Total},
		{"input_tokens_details.cached_tokens", usage.Cached},
	} {
		if usage.seen&(1<<index) != 0 {
			_ = out.Set(field.path, field.value)
		}
	}
	return out.Bytes()
}

// A positive partial snapshot is evidence, but not the final input/output meter.
func GeminiCompleteUsage(root ojson.Result) bool {
	var snapshot GeminiUsageSnapshot
	return snapshot.Merge(root, true) && snapshot.Complete
}
