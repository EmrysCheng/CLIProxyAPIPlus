# Product development

- Read docs/PROJECT_PLAN_CN.md and docs/IMPLEMENTATION_STATUS_CN.md before changing integration architecture.
- upstream/ contains immutable source snapshots. Never edit them in place.
- Store embedding changes in patches/. scripts/project.py prepares isolated build sources.
- Never bypass Manager authorization for external management requests.
- Never substitute global HTTP transports. Internal calls must use the injected transport.
- Use gofmt and run product tests plus a binary build after Go changes.
- Do not describe the P0 prototype as a production release.

# Response & change presentation

- 每次改动文件后，必须在回复中列举所有改动文件的完整清单。
- 每个改动文件必须使用 `<details><summary>` 下拉折叠块展示详细的对比改动内容（diff），以便在客户端中点击下拉展开查看变更对比。
