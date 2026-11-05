import { useCallback, useEffect, useRef, useState } from 'react';
import axios from 'axios';
import { useAuthStore } from '@/stores/useAuthStore';

export type Identity = { version: string; phase: string; core_version: string; core_commit: string; manager_version: string; manager_commit: string };
export type Asset = { name: string; sha256: string; url: string };
export type ReleaseStatus = { current_version: string; configured: boolean; state: string; stale?: boolean; error?: string; last_checked?: string; latest?: { version: string; migration_required: boolean; notes: string; assets: Asset[] } };
export type UpstreamTarget = { version: string; commit: string };
export type UpstreamProject = { id: string; name: string; repository: string; current_version: string; current_commit: string; latest_version?: string; latest_commit?: string; state: string; stale: boolean; error?: string; last_checked?: string };
export type UpstreamStatus = { enabled: boolean; reason: string; mode: string; projects: UpstreamProject[]; tools: { origin?: string; ready: boolean; missing: string[]; versions: Record<string, string> } };
export type BuildProgress = { step: string; completed: number; total: number; step_started_at: string; last_activity_at: string };
export type Operation = { progress?: BuildProgress | null; failure_code?: string; updated_at?: string; id: string; kind: string; state: string; from_version: string; target_version: string; created_at: string; error?: string; rollback_available: boolean; from_upstreams?: Record<string, UpstreamTarget>; target_upstreams?: Record<string, UpstreamTarget> };
export type OperationsStatus = { enabled: boolean; reason: string; platform: string; busy: boolean; history: Operation[] };

export function updateError(cause: unknown) {
  const detail = (cause as { response?: { data?: { error?: unknown } } })?.response?.data?.error;
  return typeof detail === 'string' ? detail : (cause instanceof Error ? cause.message : String(cause));
}

export function useProductReleases(interval = 60000) {
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey);
  const base = (apiBase || window.location.origin).replace(/\/v0\/management\/?$/, '').replace(/\/+$/, '');
  const [identity, setIdentity] = useState<Identity | null>(null);
  const [status, setStatus] = useState<ReleaseStatus | null>(null);
  const [upstreams, setUpstreams] = useState<UpstreamStatus | null>(null);
  const [operations, setOperations] = useState<OperationsStatus | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [connectionRevision, setConnectionRevision] = useState(0);
  const activeScope = useRef<object | null>(null);
  const nextLoad = useRef(0);
  const applied = useRef<Record<string, number>>({});
  const load = useCallback(async (check = false, background = false) => {
    const scope = activeScope.current;
    if (!managementKey || !scope) return;
    if (!background) setBusy(true);
    try {
      const headers = { Authorization: `Bearer ${managementKey}` };
      const sequence = ++nextLoad.current;
      const apply = <T,>(key: string, setter: (value: T) => void) => (response: { data: T }) => {
        if (activeScope.current === scope && sequence >= (applied.current[key] ?? 0)) { applied.current[key] = sequence; setter(response.data); }
      };
      const results = await Promise.allSettled([
        axios.get<Identity>(`${base}/plus-api/version`, { timeout: 5000 }).then(apply('identity', setIdentity)),
        axios.request<ReleaseStatus>({ url: `${base}/plus-api/releases${check ? '/check' : ''}`, method: check ? 'POST' : 'GET', headers, timeout: 20000 }).then(apply('release', setStatus)),
        axios.request<OperationsStatus>({ url: `${base}/plus-api/releases/operations`, method: 'GET', headers, timeout: 5000 }).then(apply('operations', setOperations)),
        axios.request<UpstreamStatus>({ url: `${base}/plus-api/releases/upstreams${check ? '/check' : ''}`, method: check ? 'POST' : 'GET', headers, timeout: check ? 90000 : 5000 }).then(apply('sources', (value: UpstreamStatus) => setUpstreams(Array.isArray(value?.projects) ? value : null))),
      ]);
      if (activeScope.current === scope && sequence >= (applied.current.error ?? 0)) {
        applied.current.error = sequence;
        const failed = results.find((result): result is PromiseRejectedResult => result.status === 'rejected');
        setError(failed ? updateError(failed.reason) : '');
      }
    } catch (cause) { if (activeScope.current === scope) setError(updateError(cause)); }
    finally { if (!background && activeScope.current === scope) setBusy(false); }
  }, [base, managementKey]);
  useEffect(() => {
    const scope = {};
    activeScope.current = scope;
    setConnectionRevision((value) => value + 1);
    setIdentity(null); setStatus(null); setUpstreams(null); setOperations(null); setError('');
    void load();
    const timer = setInterval(() => { void load(false, true); }, interval);
    return () => { clearInterval(timer); if (activeScope.current === scope) activeScope.current = null; };
  }, [load, interval]);
  const execute = useCallback(async (kind: 'install' | 'rollback', data: Record<string, unknown>) => {
    const scope = activeScope.current;
    const response = await axios.request<Operation>({ url: `${base}/plus-api/releases/operations/${kind}`, method: 'POST', data, headers: { Authorization: `Bearer ${managementKey}`, 'Content-Type': 'application/json' }, timeout: 30000 });
    if (!scope || activeScope.current !== scope) throw new Error('Connection changed; inspect the operation on the original connection.');
    void load(false, true);
    return response.data;
  }, [base, managementKey, load]);
  const executeSource = useCallback(async (data: Record<string, unknown>) => {
    const scope = activeScope.current;
    const response = await axios.request<Operation>({ url: `${base}/plus-api/releases/upstreams/install`, method: 'POST', data, headers: { Authorization: `Bearer ${managementKey}`, 'Content-Type': 'application/json' }, timeout: 90000 });
    if (!scope || activeScope.current !== scope) throw new Error('Connection changed; inspect the operation on the original connection.');
    void load(false, true);
    return response.data;
  }, [base, managementKey, load]);
  return { identity, status, upstreams, operations, error, busy, load, execute, executeSource, connectionRevision };
}
