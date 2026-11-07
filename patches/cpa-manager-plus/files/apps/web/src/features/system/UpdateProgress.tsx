import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import type { Operation } from './useProductReleases';
import styles from './ProductReleasePage.module.scss';

import { duration, failures, sourceSteps, isTerminalOperation } from './updateProgressModel';

export function UpdateProgress({ operation, stateLabel }: { operation: Operation; stateLabel: string }) {
  const { i18n } = useTranslation();
  const zh = i18n.language.startsWith('zh');
  const text = (cn: string, en: string) => zh ? cn : en;
  const [now, setNow] = useState(() => Date.now());
  const active = !isTerminalOperation(operation.state);
  useEffect(() => {
    if (!active) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [active, operation.id]);
  const progress = operation.progress;
  const stage = sourceSteps.find(([id]) => id === progress?.step);
  const buildActive = active && ['fetching_source', 'validating_source', 'building_source'].includes(operation.state);
  const end = active ? now : Date.parse(operation.updated_at ?? '');
  const message = operation.failure_code && failures[operation.failure_code];
  const fallbackMessage = operation.error?.startsWith('Maintenance did not complete normally')
    ? text('升级维护未正常完成，请根据操作编号查看私有日志。', operation.error)
    : operation.error;
  return <Card title={text('升级进度与诊断', 'Upgrade progress and diagnostics')} className={styles.operationCard}>
    <div className={styles.progressHeader}><strong role="status" className={styles.progressCurrent}>{buildActive && stage ? stage[zh ? 1 : 2] : stateLabel}</strong><span className={styles.secondaryText}>{text('总耗时', 'Elapsed')} {duration(operation.created_at, end)}</span></div>
    <div className={styles.progressMeta}><span>{operation.from_version} → {operation.target_version}</span><code>{operation.id}</code></div>
    {operation.target_upstreams && <p>{Object.entries(operation.target_upstreams).map(([name, target]) => `${name}: ${target.version} (${target.commit.slice(0, 12)})`).join(' · ')}</p>}
    {progress && <>
      <progress className={styles.progressBar} aria-label={text('已完成的构建检查阶段', 'Completed build-check stages')} max={progress.total} value={progress.completed} aria-valuetext={text(`已完成 ${progress.completed}/${progress.total} 个构建检查阶段`, `${progress.completed}/${progress.total} build-check stages complete`)} />
      <div className={styles.progressMeta}><span>{text(`已完成 ${progress.completed}/${progress.total} 个构建检查阶段（不是耗时百分比）`, `${progress.completed}/${progress.total} build-check stages complete (not a time estimate)`)}</span>{buildActive && <span>{text('本阶段耗时', 'Stage elapsed')} {duration(progress.step_started_at, end)}</span>}</div>
      {active && <p className={styles.secondaryText}>{text('上次构建活动距今', 'Time since last build activity')} {duration(progress.last_activity_at, now)} · {text('依赖下载和测试可能较久；无新输出不代表已卡死。', 'Dependency downloads and tests can take time; silence does not imply a stalled process.')}</p>}
      <details className={styles.toolDetails}><summary>{text('查看构建检查阶段', 'View build-check stages')}</summary><ol className={styles.progressSteps}>{sourceSteps.map(([id, cn, en], index) => <li key={id} data-state={index < progress.completed ? 'complete' : id === progress.step ? 'current' : 'pending'}>{index < progress.completed ? '✓ ' : `${index + 1}. `}{text(cn, en)}</li>)}</ol></details>
    </>}
    {operation.error && <div role="alert" className={styles.warning}><p className={styles.error}>{message ? message[zh ? 0 : 1] : fallbackMessage}</p>{stage && <p className={styles.secondaryText}>{text('最后构建阶段：', 'Last build stage: ')}{stage[zh ? 1 : 2]}</p>}<p className={styles.secondaryText}>{text('私有详细日志保留在该操作目录的 source-build.log / worker.log；页面不公开凭据或原始日志。', 'Private details remain in source-build.log / worker.log for this operation; credentials and raw logs are not published.')}</p></div>}
    {active && <p className={styles.secondaryText}>{text('构建通过后才会备份、切换和重启。页面短暂断线时会继续重连，请勿重复提交或另开实例。', 'Backup, replacement and restart follow successful build checks. The page reconnects during restarts; do not submit again or start another instance.')}</p>}
  </Card>;
}
