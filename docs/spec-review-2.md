# Spec Review Round 2

## 1. §5.D.5 budget `[2.0s, 3.0s]` is inconsistent with §859 jitter math

§859 specifies three claim attempts with base delays `100ms, 400ms, 1.6s` and per-attempt uniform jitter `[0.75x, 1.25x]` (per §5.D.1). The base sum is `2.1s`; with independent per-attempt jitter the achievable total-delay range is `[0.75*0.1 + 0.75*0.4 + 0.75*1.6, 1.25*0.1 + 1.25*0.4 + 1.25*1.6] = [1.575s, 2.625s]`. §5.D.5 asserts the test must observe total elapsed time in `[2.0s, 3.0s]`. The lower bound excludes ~half the legal jitter distribution (any run where attempt 3 jitter rolls below `~1.2x` can land below 2.0s) and the upper bound `3.0s` is unreachable. Either the budget bounds or the jitter distribution must be reconciled. Also unspecified: whether the budget includes the work time of the three claim attempts themselves (fold + git ops) or only the sleep delays — at 3 fold cycles this could easily add hundreds of ms.
