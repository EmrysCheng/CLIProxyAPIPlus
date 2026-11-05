import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { updateError, type UpstreamProject, type UpstreamStatus } from './useProductReleases';
import styles from './ProductReleasePage.module.scss';

type Props = { status: UpstreamStatus; maintenanceEnabled: boolean; busy: boolean; checking: boolean; check: () => void; execute: (data: Record<string, unknown>) => Promise<unknown> };
export function UpstreamSourcePanel({ status, maintenanceEnabled, busy, checking, check, execute }: Props) {
  const { i18n } = useTranslation();
  const zh = i18n.language.startsWith('zh');
  const text = (cn: string, en: string) => zh ? cn : en;
  const [selected, setSelected] = useState<{ project: string; projects: UpstreamProject[] } | null>(null);
  const [confirmation, setConfirmation] = useState('');
  const [trust, setTrust] = useState(false);
  const [acting, setActing] = useState(false);
  const [error, setError] = useState('');
  const valid = (project: UpstreamProject) => !project.stale && !!project.latest_commit && ['up_to_date', 'update_available'].includes(project.state);
  const available = (projects: UpstreamProject[]) => projects.length > 0 && projects.every(valid) && projects.some((project) => project.state === 'update_available');
  const enabled = status.enabled && maintenanceEnabled && !busy && !acting;
  const choose = (project: string, projects: UpstreamProject[]) => { setSelected({ project, projects }); setConfirmation(''); setTrust(false); setError(''); };
  const expected = selected?.projects.map((project) => `${project.name} ${project.latest_version}`).join(' + ') ?? '';
  const confirm = async () => {
    if (!selected || confirmation !== expected || !trust) return;
    setActing(true); setError('');
    try {
      await execute({ project: selected.project, targets: Object.fromEntries(selected.projects.map((project) => [project.id, project.latest_commit])), confirmation, trust_source_code: true });
      setSelected(null);
    } catch (cause) { setError(updateError(cause)); }
    finally { setActing(false); }
  };
  return <section className={styles.sourceSection} aria-label={text('基础项目自更新', 'Direct upstream updates')}>
    <Card className={`${styles.bodyCard} ${styles.sourceIntro}`} title={text('直接更新两个基础项目', 'Update the two upstream projects directly')}>
      <p>{text('直接检查固定 GitHub 仓库的最新正式版；无需发行索引或额外服务。更新源码后会重新构建本整合程序，不安装两个上游的原版 EXE。', 'Check stable releases in the fixed GitHub repositories. No release index or extra service is needed. Source changes rebuild this integrated application, not standalone upstream executables.')}</p>
      <div className={styles.actions}><Button onClick={check} loading={checking}>{text('检查上游更新', 'Check upstream updates')}</Button><Button disabled={!enabled || !available(status.projects)} onClick={() => choose('all', status.projects)}>{text('一起更新两个项目', 'Update both projects')}</Button></div>
      {!status.enabled && <p className={styles.warning}>{status.reason}</p>}
      {!status.tools?.ready && <p>{text('缺少构建工具：', 'Missing build tools: ')}{status.tools?.missing?.join(', ') || text('正在检查', 'Checking')}</p>}
      <p>{status.tools?.origin === 'bundled' ? text('构建工具来源：安装包内置（校验通过后使用，不受系统旧版本影响）', 'Build tools: bundled SDKs (verified before use; system versions are ignored)') : text('构建工具来源：自定义路径或系统环境', 'Build tools: custom paths or system environment')}</p>
      <details className={styles.toolDetails}><summary>{text('检测到的构建工具版本', 'Detected build tool versions')}</summary><ul>{Object.entries(status.tools?.versions ?? {}).map(([name, version]) => <li key={name}><strong>{name}</strong> <code>{version}</code></li>)}</ul></details>
      <p>{text('需要操作者显式开启 --web-updates --source-updates。完整安装包已内置 Python / Go / Node / npm，无需另行下载或配置 PATH；获取上游源码和项目依赖仍需联网。只提醒，不会自动安装。', 'The operator must explicitly enable --web-updates --source-updates. Complete packages include Python / Go / Node / npm; no separate SDK download or PATH setup is needed. Source and project dependencies still require network access. Notifications never install automatically.')}</p>
    </Card>
    {status.projects.map((project) => <Card key={project.id} className={styles.bodyCard} title={project.name}>
      <p><a href={`https://github.com/${project.repository}`} target="_blank" rel="noopener noreferrer">{project.repository}</a></p>
      <dl className={styles.versions}><dt>{text('当前版本', 'Running version')}</dt><dd>{project.current_version}<code>{project.current_commit}</code></dd><dt>{text('最新正式版', 'Latest stable')}</dt><dd>{project.latest_version || '…'}<code>{project.latest_commit}</code></dd></dl>
      <p role="status">{project.stale ? text('尚未成功检查，禁止安装。', 'Not freshly checked; installation is blocked.') : project.state === 'update_available' ? text('发现可更新的上游版本。', 'An upstream update is available.') : project.state === 'up_to_date' ? text('当前与最新正式版一致。', 'The running commit matches the latest stable release.') : text('版本存在差异，需要人工核对，禁止自动降级。', 'The release differs; review it manually. Automatic downgrade is blocked.')}</p>
      {project.error && <p className={styles.warning}>{project.error}</p>}
      <Button disabled={!enabled || !available([project])} onClick={() => choose(project.id, [project])}>{text(`更新 ${project.name}`, `Update ${project.name}`)}</Button>
    </Card>)}
    {selected && <section className={styles.confirmation} aria-labelledby="source-update-confirm">
      <h2 id="source-update-confirm">{text('确认源码更新、构建并重启', 'Confirm source build and restart')}</h2>
      <p>{text('仅更新选中的上游，另一项目固定在当前提交。构建期间旧服务继续运行；兼容性验证通过后备份并切换。首次构建可能需要较长时间和网络下载，补丁不兼容会失败并保留旧服务。', 'Only the selected upstream changes; its partner stays pinned. The old service runs during the build. Backup and replacement happen only after compatibility validation. Builds may take time and download dependencies; incompatible patches fail without stopping the old service.')}</p>
      <label className={styles.field}>{text(`输入 ${expected} 确认`, `Type ${expected} to confirm`)}<input value={confirmation} onChange={(event) => setConfirmation(event.target.value)} autoComplete="off" /></label>
      <label className={styles.checkbox}><input type="checkbox" checked={trust} onChange={(event) => setTrust(event.target.checked)} />{text('我信任这两个固定仓库的目标源码，理解构建/测试会以服务账户执行其代码，并接受短暂停机及成对数据快照回滚。', 'I trust the target source in these fixed repositories, understand build/tests execute it as the service account, and accept brief downtime and paired snapshot rollback.')}</label>
      {acting && <p role="status" className={styles.secondaryText}>{text('正在提交并重新核对上游版本和构建工具；受理后将在页面顶部显示构建阶段，请勿重复提交。', 'Submitting and rechecking upstream versions and SDKs. Accepted builds appear at the top of the page; do not submit again.')}</p>}
      {error && <p role="alert" className={styles.error}>{error}</p>}
      <div className={styles.actions}><Button disabled={!enabled || confirmation !== expected || !trust} loading={acting} onClick={() => void confirm()}>{text('确认构建并升级', 'Confirm build and upgrade')}</Button><Button variant="secondary" disabled={acting} onClick={() => setSelected(null)}>{text('取消', 'Cancel')}</Button></div>
    </section>}
  </section>;
}
