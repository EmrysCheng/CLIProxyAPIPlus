import { useTranslation } from 'react-i18next';
import { useProductReleases } from './useProductReleases';
import styles from './ProductReleasePage.module.scss';

export function ProductUpdateNotice() {
  const { i18n } = useTranslation();
  const zh = i18n.language.startsWith('zh');
  const { status, upstreams, operations } = useProductReleases();
  const failed = operations?.history?.[0];
  const attention = failed && (failed.state === 'recovery_required' || failed.error);
  const sourceUpdates = upstreams?.projects.filter((project) => project.state === 'update_available' && !project.stale) ?? [];
  const available = (status?.state === 'update_available' && !status.stale) || sourceUpdates.length > 0;
  if (!available && !attention) return null;
  return <aside className={styles.notice} role="status">
    <span>{attention
      ? (zh ? '上次程序维护未正常完成，请查看升级记录。' : 'The last maintenance operation needs attention. Review its history.')
      : sourceUpdates.length > 0 ? (zh ? `基础项目可更新：${sourceUpdates.map((project) => `${project.name} ${project.latest_version}`).join('、')}` : `Upstream updates available: ${sourceUpdates.map((project) => `${project.name} ${project.latest_version}`).join(', ')}`) : (zh ? `CLIProxyAPIPlus ${status!.latest?.version} 可升级。` : `CLIProxyAPIPlus ${status!.latest?.version} is available.`)}</span>
    <a href="#/system/updates">{zh ? '查看程序更新' : 'Review application updates'}</a>
  </aside>;
}
