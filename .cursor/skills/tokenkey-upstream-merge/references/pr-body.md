## 5. PR body 模板

```markdown
## Summary
- Merge upstream/main into TokenKey with a merge commit while preserving TokenKey OPC invariants.
- Keep upstream features compiled in; TokenKey-specific behavior stays behind companion/facade/component boundaries.

## Risk
- Large upstream merge across: <hotspots>.
- Human-reviewed decisions: <decision list>.

## Validation
- <commands run>

## Upstream Audit
- Required audit range: upstream/main..HEAD
- TK ahead count: `<git log --oneline upstream/main..HEAD | wc -l>`
- Backend stat top files: `<git diff --stat upstream/main..HEAD -- backend/ | head -5>`
```
