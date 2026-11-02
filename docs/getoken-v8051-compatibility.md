# Getoken CPA v8.0.51 integration

This merge joins official v8.0.51 (`7cad1cb7`) with Getoken v8.0.44+g11 (`2e958dc5`). It retains the custom metering, transport, pool and suspension safeguards rather than replacing the fork.

## Retained boundaries

- Antigravity credentials are permanently disabled only for structured provider `TOS_VIOLATION` responses. Generic permission errors and model quota exhaustion are separate conditions. Late successful completions still count, but cannot enable or mark a disabled credential inference-verified.
- Ultra/Pro capability caches include the pool prefix. Moving a credential requires entitlement revalidation, without restoring disabled credentials.
- Empty responses fail while preserving real meter evidence. Incomplete, missing or invalid final counters are not fabricated as zero. Responses terminal conversion retains late usage and a single terminal event.
- Gemini partial tool arguments and thought signatures use the official assembler. A malformed tool response remains failed; its output item is incomplete. No terminal success is emitted ahead of a final usage tail.
- Progress events preserve activity without synthesizing model output or usage. HTTP/1.1 protocol restrictions, OAuth proxy transport and inbound header isolation remain intact.
- Schema extension removal visits schema nodes only, preserving user property names and data values.
- The retained dependency versions are compress 1.18.7 and redis 9.20.0; release builds use Go 1.27.3. The compression bomb regression explicitly validates a single-segment frame instead of depending on an older encoder default.

## Empty configuration read protection

A truncate/write save can race the second read in the configuration watcher. Strict loading now rejects an empty or whitespace-only snapshot, preserving the last valid runtime configuration. Optional cloud-standby loading still accepts an empty file. This closes the reproduced empty-file window; it is not a claim that every externally written, syntactically valid partial YAML document is detected.

## Release requirements

The plugin C ABI remains 1 while the host RPC schema advances from 7 to 10. New fields are capability/version gated. Source tests do not prove compatibility of deployed closed-source plugins: validate both original binary hashes in a Linux candidate with no production auth directory and no external network access before switching.

Preserve configuration, auth files, pool prefixes, suspension and quota state, usage-flush behavior, and current memory limits. Drain in-flight requests before replacing the single auth-state writer. Keep a tested rollback image; do not restore an older auth directory or ledger. Production model support and accounting require their own exact trace and usage evidence after deployment.
