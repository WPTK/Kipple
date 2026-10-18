# Triage: how a bug gets its priority

Every bug-type issue (`bug`, `security`, `accessibility`, `performance`, `ux`, `test`) carries exactly one priority
label, `P0` to `P4`, in any milestone. No bug is filed or left without one. Enhancements and `roadmap` items are not
bugs and get none.

The scale is the one in [uat-plan.md](uat-plan.md#defect-severity-borrowed-scale), plus P4:

| Priority | Meaning | Shipping rule |
|---|---|---|
| P0 Blocker | Data loss, security bypass, can't complete a core flow | Must fix before sign-off |
| P1 High | Wrong behavior a normal session would hit | Must fix before sign-off |
| P2 Medium | Edge case or cosmetic-but-real bug | Fix, or the owner explicitly waives it |
| P3 Low | Polish | Fix, or the owner explicitly waives it |
| P4 Lowest | Trivial polish, nits, wording, docs mismatches | Fix when convenient; may stay open |

Performance and UX problems get a priority by user impact, plus the `performance` or `ux` label.

- The bug report form asks for a severity; the maintainer applies the matching label when the issue is filed.
- Nothing is silently dropped: a deferral is a `roadmap` or `wontfix` label with a one-line reason.
- Check for gaps: `gh issue list --state open --label bug --search "-label:P0 -label:P1 -label:P2 -label:P3 -label:P4"`
  returns nothing.
