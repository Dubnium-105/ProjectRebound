# Relay V1.1 Security Test Suite

English | [简体中文](relay-security-tests.zh-CN.md)

run:

```bash
cd Backend
bash scripts/test/relay-security.sh
```

The first pass uses the Go race detector. The second repeats key state machines 20 times to expose unstable behavior around random handles, time windows, and rate-limit boundaries.

|Threat/Constraint|Automated testing|
| --- | --- |
| Unverified addresses receive no state, and challenges are not amplified | `TestRuntimeCookieBindingAndAuthorizedForwarding`, `TestV2CookieIsStatelessAndAcceptsCurrentOrPreviousBucket` |
|Error Cookie|Same as above|
|Forgery/tampering of Token, wrong node, expiration, future nbf, wrong role|`TestRuntimeRejectsWrongNodeExpiredAndInvalidRoleTokens` with token verifier tests|
|`jti` Cross-IP replay, NAT short-term port change| `TestRuntimeAllowsShortNATPortRebindButRejectsLateOrCrossIPReplay` |
|Data authentication tags, source endpoint impersonation, unknown flags| `TestRuntimeCookieBindingAndAuthorizedForwarding` |
|Any target forwarding|The packet protocol has no destination field; testing confirmed that the output address can only be the other end to which the same allocation is bound|
|sequence replay/old|`TestRuntimeCookieBindingAndAuthorizedForwarding` and replay-window unit path|
|MTU/extra large package| `TestRuntimeCookieBindingAndAuthorizedForwarding` |
|PPS, BPS, total bytes, absolute/idle expiration| `TestRuntimeRateLimitsTotalBytesAndExpiresInMemoryAllocations` |
| Invalid-token flooding and IP-state-table capacity | `TestRuntimeTemporarilyBansInvalidTokenFlood`, `TestRuntimeSeparatesUnverifiedLimitsAndBoundsIPState` |
|Single IP allocation limit| `TestRuntimeLimitsUniqueAllocationsPerIP` |
|handle becomes invalid after allocation is closed| total-byte/expiry tests |
|Overload protection retains existing connections| `TestRuntimeOverloadStateRejectsOnlyNewAllocations` |

These tests use an in-process virtual clock and a real UDP listener and do not falsify the conclusion that source address spoofing can traverse the Internet. Public network source address filtering still relies on the host firewall and upstream network; Relay's own security boundaries are cookie address ownership verification, Token scope, binding source check, and no-response discarding.
