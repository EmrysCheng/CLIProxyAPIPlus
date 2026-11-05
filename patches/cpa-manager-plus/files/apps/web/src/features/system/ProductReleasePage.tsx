import { UpstreamSourcePanel } from './UpstreamSourcePanel';
import { useEffect, useRef, useState } from 'react';
import { UpdateProgress } from './UpdateProgress';
import { isTerminalOperation } from './updateProgressModel';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { useProductReleases, updateError, type Operation } from './useProductReleases';
import styles from './ProductReleasePage.module.scss';

type Pending = { kind: 'install' | 'rollback'; version: string; sha256?: string; operationId?: string };
const phases: Record<string, [string, string]> = {
  fetching_source: ['准备固定提交的源码', 'Fetching pinned sources'], validating_source: ['构建与兼容性验证', 'Building and validating sources'], building_source: ['构建整合程序', 'Building integrated application'],
  verified: ['产物校验通过', 'Artifact verified'], queued: ['等待执行', 'Queued'], downloading: ['下载发行包', 'Downloading'], verifying: ['校验发行包', 'Verifying'],
  awaiting_shutdown: ['等待请求排空与停机', 'Draining and stopping'], backing_up: ['备份程序与数据', 'Backing up'],
  installing: ['替换程序', 'Installing'], checking_data: ['检查数据与迁移', 'Checking data'], restarting: ['重启并检查健康状态', 'Restarting and checking health'],
  restoring: ['恢复匹配的数据快照', 'Restoring snapshot'], succeeded: ['升级成功', 'Upgrade completed'],
  rolled_back: ['已恢复升级前状态', 'Previous state restored'], failed: ['未安装，当前版本保留', 'Not installed; current version retained'],
  recovery_required: ['需要离线恢复，请勿删除维护标记', 'Offline recovery required; preserve the maintenance marker'],
};

export function ProductReleasePage() {
  const { i18n } = useTranslation();
  const zh = i18n.language.startsWith('zh');
  const text = (cn: string, en: string) => zh ? cn : en;
  const { identity, status, upstreams, operations, error, busy, load, execute, executeSource, connectionRevision } = useProductReleases(3000);
  const [pending, setPending] = useState<Pending | null>(null);
  const [confirmation, setConfirmation] = useState('');
  const [acceptLoss, setAcceptLoss] = useState(false);
  const [acting, setActing] = useState(false);
  const [actionError, setActionError] = useState('');
  const [accepted, setAccepted] = useState<Operation | null>(null);
  const actionOwner = useRef(0);
  useEffect(() => {
    actionOwner.current += 1;
    setAccepted(null); setPending(null); setActionError(''); setActing(false); setConfirmation(''); setAcceptLoss(false);
    return () => { actionOwner.current += 1; };
  }, [connectionRevision]);
  const platformSuffix = operations?.platform.replace('/', '_');
  const matching = (status?.latest?.assets ?? []).filter((asset) => platformSuffix && asset.name.endsWith(`_${platformSuffix}_portable${platformSuffix.startsWith('windows') ? '.zip' : '.tar.gz'}`)) ?? [];
  const asset = matching.length === 1 ? matching[0] : undefined;
  const history = operations?.history ?? [];
  const acceptedUnseen = accepted && !isTerminalOperation(accepted.state) && !history.some((operation) => operation.id === accepted.id);
  const maintenanceBusy = !!operations?.busy || acting || !!acceptedUnseen || history.some((operation) => !isTerminalOperation(operation.state));
  const canInstall = operations?.enabled && !maintenanceBusy && status?.state === 'update_available' && !status.stale && asset;
  const choose = (value: Pending) => { setPending(value); setConfirmation(''); setAcceptLoss(false); setActionError(''); };
  const confirm = async () => {
    if (!pending || confirmation !== pending.version || (pending.kind === 'rollback' && !acceptLoss)) return;
    const owner = ++actionOwner.current;
    setActing(true); setActionError('');
    try {
      const operation = await execute(pending.kind, { version: pending.version, sha256: pending.sha256, operation_id: pending.operationId, confirmation, accept_data_loss: acceptLoss });
      if (owner === actionOwner.current) { setAccepted(operation); setPending(null); }
    } catch (cause) { if (owner === actionOwner.current) setActionError(updateError(cause)); }
    finally { if (owner === actionOwner.current) setActing(false); }
  };
  const progressAnchor = useRef<HTMLElement>(null);
  useEffect(() => { if (accepted?.id) progressAnchor.current?.scrollIntoView({ block: 'start', behavior: 'auto' }); }, [accepted?.id]);
  const startSource = async (data: Record<string, unknown>) => {
    const owner = ++actionOwner.current;
    setActing(true);
    try {
      const operation = await executeSource(data);
      if (owner !== actionOwner.current) throw new Error('Connection changed; inspect the operation on the original connection.');
      setAccepted(operation);
      return operation;
    } finally { if (owner === actionOwner.current) setActing(false); }
  };
  const latest = history.find((operation) => !isTerminalOperation(operation.state))
    ?? (accepted && !isTerminalOperation(accepted.state) && !history.some((operation) => operation.id === accepted.id) ? accepted : history[0] ?? accepted);
  return <main className={styles.page}>
    <header className={styles.heading}><div><h1>{text('程序升级与回滚', 'Application upgrades and rollback')}</h1><p>{text('两个上游，一份经过验证的整合程序。', 'Two upstreams. One verified application release.')}</p></div>
      <Button onClick={() => void load(true)} loading={busy}>{text('检查发行版本', 'Check releases')}</Button></header>
    {error && <p role="alert" className={styles.error}>{text('暂时无法读取服务状态，将自动重试。重启期间短暂断线是正常的；回滚后可能需要使用原管理员凭据重新登录。', 'Service status is unavailable; retrying automatically. A restart briefly disconnects the page; rollback may require the previous admin credential.')} {error}</p>}
    {latest && <section ref={progressAnchor} className={styles.operationCard} aria-label={text('当前升级状态', 'Current upgrade status')}><UpdateProgress operation={latest} stateLabel={latest.progress && latest.state === 'downloading' ? text('准备本地构建产物', 'Preparing the local build artifact') : (phases[latest.state] ?? [latest.state, latest.state])[zh ? 0 : 1]} /></section>}
    {upstreams && <UpstreamSourcePanel key={connectionRevision} status={upstreams} maintenanceEnabled={!!operations?.enabled} busy={maintenanceBusy} checking={busy} check={() => void load(true)} execute={startSource} />}
    <Card className={styles.bodyCard} title="CLIProxyAPIPlus"><dl className={styles.versions}>
      <dt>{text('程序版本', 'Application')}</dt><dd>{identity?.version ?? '…'} <span>{identity?.phase}</span></dd>
      <dt>CLIProxyAPI</dt><dd>{identity?.core_version ?? '…'}<code>{identity?.core_commit}</code></dd>
      <dt>CPA-Manager-Plus</dt><dd>{identity?.manager_version ?? '…'}<code>{identity?.manager_commit}</code></dd>
    </dl></Card>
    {(!upstreams || status?.configured) && <>    <Card className={styles.bodyCard} title={text('可用发行版本', 'Available release')}>
      {!status?.configured ? <p>{text('尚未配置本项目的发行索引。请配置可信 HTTPS 整包发行源，或在启动时显式开启本机回环 HTTP 更新源；不会直接下载原版 CPA、Manager 或单独的网页。', 'No product release index is configured. Configure a trusted HTTPS integrated release source, or explicitly enable a loopback HTTP release source at startup; standalone CPA, Manager or HTML downloads are not installed.')}</p>
        : <p>{status.latest ? (status.state === 'update_available' ? text(`发现新版本 ${status.latest.version}`, `Release ${status.latest.version} is available`) : text(`无需升级；索引版本 ${status.latest.version}`, `No upgrade required; indexed release ${status.latest.version}`)) : text('正在等待发行信息，或点击检查重新获取。', 'Waiting for release metadata. Check releases to retry.')}</p>}
      {status?.error && <p role="status" className={styles.warning}>{status.error}</p>}
      {status?.stale && status.latest && <p className={styles.warning}>{text('发行信息已过期，重新检查成功后才能升级。', 'Release metadata is stale. A successful check is required before installation.')}</p>}
      {status?.latest?.notes && <p className={styles.notes}>{status.latest.notes}</p>}
      {status?.latest?.migration_required && <p className={styles.warning}>{text('此版本涉及数据迁移。系统会先停机并备份程序、配置、数据库及密钥；失败时成对恢复。', 'This release migrates data. The application backs up binary, config, database and keys while stopped, and restores the matching pair on failure.')}</p>}
      {operations && !operations.enabled && <p className={styles.warning}>{text('当前部署未开启网页执行能力：', 'Web installation is unavailable: ')}{operations.reason}</p>}
      {status?.state === 'update_available' && operations?.enabled && !asset && <p className={styles.warning}>{text('此发行没有唯一匹配当前平台的 Portable 包，不能在线安装。', 'No unique Portable package matches this platform; web installation is blocked.')}</p>}
      <div className={styles.actions}><Button disabled={!canInstall || acting} onClick={() => status?.latest && asset && choose({ kind: 'install', version: status.latest.version, sha256: asset.sha256 })}>{text('升级并重启', 'Upgrade and restart')}</Button><span>{text('需要短暂停机，不会自动安装。', 'Brief downtime. Installation is never automatic.')}</span></div>
      {asset && <details><summary>{text('发行包与校验和', 'Package and checksum')}</summary><p className={styles.asset}><a href={asset.url} target="_blank" rel="noopener noreferrer">{asset.name}</a><code>SHA-256 {asset.sha256}</code></p></details>}
    </Card></>}
    {pending && <section className={styles.confirmation} aria-labelledby="update-confirm-title">
      <h2 id="update-confirm-title">{pending.kind === 'install' ? text('确认升级并重启', 'Confirm upgrade and restart') : text('确认恢复旧程序与旧数据', 'Confirm program and data rollback')}</h2>
      <p>{pending.kind === 'install' ? text('请求将正常排空，然后备份并安装整合程序。浏览器可能短暂断线，请勿重复提交。', 'Requests drain before backup and installation. The page briefly disconnects. Do not submit twice.') : text('回滚不是只替换程序：会恢复升级前的配置、认证与数据库。快照之后的修改和用量将不再出现在恢复后的系统中，当前数据会另行保留供人工核对。', 'Rollback restores pre-upgrade configuration, credentials and database, not just the binary. Changes and usage since that snapshot disappear from the restored system; current files are retained separately for inspection.')}</p>
      <label className={styles.field}>{text(`输入目标版本 ${pending.version} 确认`, `Type target version ${pending.version} to confirm`)}<input autoFocus value={confirmation} onChange={(event) => setConfirmation(event.target.value)} autoComplete="off" /></label>
      {pending.kind === 'rollback' && <label className={styles.checkbox}><input type="checkbox" checked={acceptLoss} onChange={(event) => setAcceptLoss(event.target.checked)} />{text('我理解并接受恢复快照后的数据变化。', 'I understand and accept restoring snapshot data.')}</label>}
      {actionError && <p role="alert" className={styles.error}>{actionError}</p>}
      <div className={styles.actions}><Button variant={pending.kind === 'rollback' ? 'danger' : 'primary'} disabled={confirmation !== pending.version || (pending.kind === 'rollback' && !acceptLoss)} loading={acting} onClick={() => void confirm()}>{pending.kind === 'install' ? text('确认升级并重启', 'Confirm upgrade and restart') : text('确认回滚并重启', 'Confirm rollback and restart')}</Button><Button variant="secondary" disabled={acting} onClick={() => setPending(null)}>{text('取消', 'Cancel')}</Button></div>
    </section>}

    <Card className={styles.bodyCard} title={text('升级记录与回滚', 'Upgrade history and rollback')}>
      {history.length === 0 && <p>{text('暂无网页升级记录。只有带有匹配数据快照的成功升级才可在此回滚。', 'No web upgrade history. Rollback requires a successful upgrade and its matching data snapshot.')}</p>}
      {history.map((operation) => <article key={operation.id} className={styles.history}><div><strong>{operation.from_version} → {operation.target_version}</strong>{operation.target_upstreams && <p>{Object.entries(operation.target_upstreams).map(([name, target]) => `${name}: ${target.version} (${target.commit.slice(0, 12)})`).join(' · ')}</p>}<p>{(phases[operation.state] ?? [operation.state, operation.state])[zh ? 0 : 1]} · {operation.created_at}</p><code>{operation.id}</code></div>{operation.rollback_available && <Button variant="secondary" disabled={!operations?.enabled || maintenanceBusy} onClick={() => choose({ kind: 'rollback', version: operation.from_version, operationId: operation.id })}>{operation.from_upstreams ? text('恢复升级前的双上游组合', 'Restore the previous upstream pair') : text(`回滚至 ${operation.from_version}`, `Rollback to ${operation.from_version}`)}</Button>}</article>)}
    </Card>
    <p className={styles.footnote}>{text('网页升级仅适用于显式开启的独立 Portable 实例。容器或服务托管部署请通过部署系统更新镜像或程序；不要混用自动拉起。发行源必须由操作者信任，SHA-256 不能替代发行签名。', 'Web installation is opt-in for standalone Portable instances. Update containers and supervised services through their deployment system; do not combine restart policies. Trust the release source: SHA-256 does not replace release signing.')}</p>
  </main>;
}
