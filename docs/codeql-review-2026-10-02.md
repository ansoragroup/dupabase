# Initial CodeQL review

The first `security-extended` scan of `afd7eec` produced 20 alerts. The release
gate checks open security alerts after SARIF processing; running the analyzer
successfully is insufficient. Alert dismissals require evidence in GitHub and
are specific to that alert, rather than a disabled query or excluded directory.

| Alerts | Assessment | Evidence and action |
| --- | --- | --- |
| 1–4, JavaScript temporary files | Fix | Replace predictable shared temporary paths with `mkdtempSync` private directories and cleanup. The legacy database fixtures now require an explicit loopback database URL and execute commands with argument arrays. |
| 6–7, pool integer narrowing | Fix | Parse configuration integers with an explicit 32-bit bound before conversions to pgx pool limits. Oversized input follows the existing invalid-input fallback. Regression tests exercise values that previously wrapped. |
| 5, copied bcrypt cost conversion | False positive | `newFromPassword` and `decodeCost` call `checkCost` before the unexported `bcrypt` function. Costs are constrained to 4–31 before `uint32(cost)`. The original upstream source and hash manifest remain unchanged. |
| 8, SHA-256 in `projectLogin` | False positive | The hash derives a stable, public PostgreSQL **role name** from the project UUID; it does not store a password. `projectURL` separately derives the password with HMAC-SHA-256 and the platform secret. Existing roles must retain their names for compatibility. |
| 9–20, structured logging | False positive | Untrusted values are attributes of constant log messages. The shipped server uses Go's default structured handler, or `slog.NewJSONHandler` with `LOG_FORMAT=json`. Go 1.27's shared handler quotes and escapes text attributes (`strconv.AppendQuote`) and JSON strings (`appendEscapedJSONString`), including embedded newlines. No handler supplied by a request replaces them. |

CodeQL continues to scan the copied crypto and test code. New alert instances
remain subject to review. PostgreSQL, API and dependency gates run separately.
