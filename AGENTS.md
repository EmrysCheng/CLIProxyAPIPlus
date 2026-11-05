# Product development

- Read docs/PROJECT_PLAN_CN.md and docs/IMPLEMENTATION_STATUS_CN.md before changing integration architecture.
- upstream/ contains immutable source snapshots. Never edit them in place.
- Store embedding changes in patches/. scripts/project.py prepares isolated build sources.
- Never bypass Manager authorization for external management requests.
- Never substitute global HTTP transports. Internal calls must use the injected transport.
- Use gofmt and run product tests plus a binary build after Go changes.
- Do not describe the P0 prototype as a production release.
