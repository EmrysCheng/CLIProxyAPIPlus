export const sourceSteps = [
  ['checking_tools', '校验包内构建工具', 'Verifying bundled SDKs'],
  ['verifying_recipe', '核对并复制源码配方', 'Verifying and copying the source recipe'],
  ['fetching_source', '获取固定提交的源码', 'Fetching pinned upstream sources'],
  ['preparing_source', '准备适配后的源码', 'Preparing adapted sources'],
  ['frontend_dependencies', '安装前端依赖', 'Installing frontend dependencies'],
  ['frontend_build', '构建管理前端', 'Building the management frontend'],
  ['product_tests', '获取 Go 依赖并运行产品测试', 'Fetching Go dependencies and testing the product'],
  ['binary_build', '编译整合程序', 'Compiling the integrated application'],
  ['packaging', '校验工具并打包候选程序', 'Verifying SDKs and packaging the candidate'],
  ['single_port_test', '验证单端口与权限', 'Checking single-port access and authorization'],
  ['update_test', '验证升级、回滚与失败恢复', 'Testing upgrade, rollback and recovery'],
  ['recipe_test', '验证下一份源码配方', 'Verifying the next source recipe'],
  ['frontend_tests', '运行前端检查与测试', 'Running frontend checks and tests'],
  ['adapter_tests', '运行适配与独立入口回归', 'Running adapter and standalone regressions'],
  ['binary_smoke', '验证候选程序运行', 'Smoke-testing the candidate application'],
  ['maintenance_smoke', '验证备份与迁移恢复', 'Testing backup and migration recovery'],
  ['verifying_artifact', '核对最终构建产物', 'Verifying the final build artifact'],
] as const;

export const failures: Record<string, [string, string]> = {
  go_dependency_network: ['Go 依赖下载失败，尚未替换程序。请为构建配置可访问的 GOPROXY 或 HTTPS_PROXY；也可在工具配置中填写 go_proxy，再手动重试。不要关闭依赖校验。', 'Go dependency download failed before installation. Configure a reachable GOPROXY or HTTPS_PROXY, or set go_proxy in the operator tools configuration, then retry manually. Keep checksum verification enabled.'],
  npm_dependency_network: ['前端依赖下载失败。请检查构建网络代理或 npm_registry 配置后手动重试。', 'Frontend dependency download failed. Check the build network proxy or npm_registry configuration before retrying.'],
  source_network: ['无法获取上游源码。请检查 GitHub 连通性或构建网络代理后手动重试。', 'Upstream source download failed. Check GitHub connectivity or the build network proxy before retrying.'],
  patch_incompatible: ['目标上游源码与当前适配补丁不兼容。需要经过验证的新配方，不能跳过补丁或测试强行升级。', 'The target upstream is incompatible with the integration patches. Use a verified compatible recipe; do not skip patches or tests.'],
  toolchain_incompatible: ['目标上游要求更新的构建工具。请安装经过验证的新 SDK 整包或由操作者配置可信工具。', 'The target upstream requires newer build tools. Install a verified SDK package or configure trusted tools.'],
  tools_unavailable: ['包内工具未通过完整性、平台或版本检查，请重新解压匹配的完整安装包。', 'Bundled SDK integrity, platform or version checks failed. Reinstall the complete matching package.'],
  recipe_invalid: ['源码配方与当前程序不匹配或校验失败，请使用随包的匹配配方。', 'The source recipe does not match this program or failed integrity checks. Use its matching packaged recipe.'],
  source_cancelled: ['构建已取消，尚未安装。', 'The build was cancelled before installation.'],
  source_timeout: ['构建超时，尚未安装。请检查网络、CPU 和磁盘空间。', 'The build timed out before installation. Check network access, CPU and disk space.'],
  source_build_failed: ['构建或兼容性验证未通过，尚未安装。请按操作编号查看本地私有构建日志。', 'The build or compatibility validation failed before installation. Inspect the private local build log using the operation ID.'],
};

export function duration(start: string | undefined, end: number) {
  const seconds = Math.max(0, Math.floor((end - Date.parse(start ?? '')) / 1000));
  if (!Number.isFinite(seconds)) return '—';
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
}
export const isTerminalOperation = (state: string) => ['succeeded', 'rolled_back', 'failed', 'recovery_required'].includes(state);

