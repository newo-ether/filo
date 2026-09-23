# Error contract

Authenticated HTTP failures and SSE `event: error` frames share a JSON envelope:

```json
{
  "code": "desktop_unavailable",
  "message": "Filo cannot reach Codex Desktop. Open Codex on the target computer.",
  "error": "Original desktop owner is unavailable; refresh before continuing"
}
```

`code` is the stable presentation key. Agora translates it using its application
locale, independent of the target computer language. `message` is an English
fallback for other API clients. `error` retains the native diagnostic for older
clients and troubleshooting; credentials and control characters are removed and
the detail is limited to 2048 Unicode code points.

The catalog lives in `internal/apierror/response.go`. Unknown native errors keep
their detail under `native_error`; clients must tolerate unknown codes and older
responses without a code. Never infer native execution failure from a missing
translation or a disconnected stream.

Error codes do not change HTTP status, ownership, native acceptance, send-button
availability or retry policy. A timeout, pending operation or unconfirmed receipt
never authorizes automatically sending the input again. Read retry remains
separate from mutation delivery reconciliation.

Transport authentication failures remain authentication failures. A provider's
own unauthorized response is a native error, not proof of an invalid Filo token.
Attachment and image errors use the same presentation contract. Shared Conch
encryption and authentication are unchanged.
