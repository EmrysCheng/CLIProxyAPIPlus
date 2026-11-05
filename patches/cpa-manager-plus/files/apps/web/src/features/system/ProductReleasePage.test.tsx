import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ProductReleasePage } from './ProductReleasePage';
import { ProductUpdateNotice } from './ProductUpdateNotice';
import { duration } from './updateProgressModel';
const mocks = vi.hoisted(() => ({ get: vi.fn(), request: vi.fn(), auth: { apiBase: 'https://local.example/v0/management', managementKey: 'admin-fixture' } }));
vi.mock('axios', () => ({ default: mocks }));
vi.mock('@/stores/useAuthStore', () => ({ useAuthStore: (selector: (s: unknown) => unknown) => selector(mocks.auth) }));
vi.mock('react-i18next', () => ({ useTranslation: () => ({ i18n: { language: 'en' } }) }));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let renderer: ReactTestRenderer | undefined;
const content = () => JSON.stringify(renderer!.toJSON());
const hash = 'a'.repeat(64);
let release: Record<string, unknown>;
let operations: Record<string, unknown>;
let upstreams: Record<string, unknown> | null;
const buttons = () => renderer!.root.findAllByType('button');
const button = (name: string) => buttons().find((item) => JSON.stringify(item.children.map((node) => typeof node === 'object' && node && 'children' in node ? node.children : node)).includes(name))!;
beforeEach(() => {
  mocks.auth.apiBase = 'https://local.example/v0/management'; mocks.auth.managementKey = 'admin-fixture';
  vi.stubEnv('VITE_CPA_PLUS_EMBEDDED','1');
  release = { configured: false, state: 'no_candidate' }; upstreams = null;
  operations = { enabled: false, reason: 'Start with --web-updates', platform: 'windows/amd64', busy: false, history: [] };
  mocks.get.mockResolvedValue({ data: { version: '0.3.0-rc.1', phase: 'release-candidate', core_version: 'v8.0.53', core_commit: 'a'.repeat(40), manager_version: 'v1.14.10', manager_commit: 'b'.repeat(40) } });
  mocks.request.mockImplementation(async (request: { url: string; method: string }) => {
    if (request.url.endsWith('/upstreams/install')) return { data: { id: 'source-operation', kind: 'install', state: 'fetching_source', from_version: '0.4.0-rc.1', target_version: '0.4.0-rc.1' } };
    if (request.url.endsWith('/upstreams') || request.url.endsWith('/upstreams/check')) return { data: upstreams };
    if (request.url.endsWith('/install') || request.url.endsWith('/rollback')) return { data: { id: 'new-operation', state: 'queued', from_version: '0.3.0-rc.1', target_version: '0.3.0-rc.2' } };
    return { data: request.url.endsWith('/operations') ? operations : release };
  });
});
afterEach(async () => { if (renderer) await act(async () => renderer!.unmount()); renderer = undefined; vi.clearAllMocks(); vi.unstubAllEnvs(); });
const available = () => {
  release = { configured: true, state: 'update_available', latest: { version: '0.3.0-rc.2', notes: '', assets: [{ name: 'CLIProxyAPIPlus_0.3.0-rc.2_windows_amd64_portable.zip', sha256: hash, url: 'https://release.example/app.zip' }] } };
  operations = { ...operations, enabled: true, reason: '' };
};
describe('Product web updates', () => {
  it('uses product metadata and Manager admin auth; disabled capability never installs', async () => {
    await act(async () => { renderer = create(<ProductReleasePage />); });
    expect(content()).toContain('0.3.0-rc.1'); expect(content()).toContain('v8.0.53');
    expect(content()).toContain('No product release index is configured');
    expect(button('Upgrade and restart').props.disabled).toBe(true);
    expect(mocks.request).toHaveBeenCalledWith(expect.objectContaining({ url: 'https://local.example/plus-api/releases/operations', headers: { Authorization: 'Bearer admin-fixture' } }));
    expect(content()).not.toContain('seakee/cpa-manager-plus/releases');
  });
  it('checks releases explicitly and reports transient reconnect errors without success', async () => {
    mocks.get.mockRejectedValueOnce(new Error('offline'));
    await act(async () => { renderer = create(<ProductReleasePage />); });
    expect(content()).toContain('offline'); expect(content()).not.toContain('Upgrade completed');
    await act(async () => { button('Check releases').props.onClick(); });
    expect(mocks.request).toHaveBeenCalledWith(expect.objectContaining({ method: 'POST', url: 'https://local.example/plus-api/releases/check' }));
    expect(content()).not.toContain('role":"alert');
  });
  it('requires typing the version and posts only the selected release identity', async () => {
    available();
    await act(async () => { renderer = create(<ProductReleasePage />); });
    await act(async () => { button('Upgrade and restart').props.onClick(); });
    expect(button('Confirm upgrade and restart').props.disabled).toBe(true);
    await act(async () => { renderer!.root.findByType('input').props.onChange({ target: { value: '0.3.0-rc.2' } }); });
    await act(async () => { button('Confirm upgrade and restart').props.onClick(); });
    expect(mocks.request).toHaveBeenCalledWith(expect.objectContaining({ url: 'https://local.example/plus-api/releases/operations/install', method: 'POST', data: expect.objectContaining({ version: '0.3.0-rc.2', sha256: hash, confirmation: '0.3.0-rc.2' }) }));
    expect(content()).toContain('Queued');
    expect(content()).not.toContain('Upgrade completed');
  });
  it('blocks stale releases and missing platform assets', async () => {
    available(); release = { ...release, stale: true };
    await act(async () => { renderer = create(<ProductReleasePage />); });
    expect(button('Upgrade and restart').props.disabled).toBe(true);
    expect(content()).toContain('Release metadata is stale');
  });
  it('requires both rollback version and explicit snapshot data-loss consent', async () => {
    operations = { ...operations, enabled: true, history: [{ id: 'previous-operation', kind: 'install', state: 'succeeded', from_version: '0.2.0', target_version: '0.3.0-rc.1', created_at: '2026-11-04T00:00:00Z', rollback_available: true }] };
    await act(async () => { renderer = create(<ProductReleasePage />); });
    await act(async () => { button('Rollback to 0.2.0').props.onClick(); });
    expect(content()).toContain('Changes and usage since that snapshot disappear');
    const inputs = renderer!.root.findAllByType('input');
    await act(async () => { inputs[0].props.onChange({ target: { value: '0.2.0' } }); });
    expect(button('Confirm rollback and restart').props.disabled).toBe(true);
    await act(async () => { inputs[1].props.onChange({ target: { checked: true } }); });
    await act(async () => { button('Confirm rollback and restart').props.onClick(); });
    expect(mocks.request).toHaveBeenCalledWith(expect.objectContaining({ url: 'https://local.example/plus-api/releases/operations/rollback', data: expect.objectContaining({ operation_id: 'previous-operation', confirmation: '0.2.0', accept_data_loss: true }) }));
  });
  it('shows global upgrade reminders only for a fresh release', async () => {
    available();
    await act(async () => { renderer = create(<ProductUpdateNotice />); });
    expect(content()).toContain('0.3.0-rc.2 is available');
    expect(renderer!.root.findByType('a').props.href).toBe('#/system/updates');
  });
});

const directSources = () => {
  operations = { ...operations, enabled: true, reason: '' };
  upstreams = { enabled: true, reason: '', mode: 'direct-upstream-source', tools: { ready: true, missing: [], versions: {} }, projects: [
    { id: 'cliproxyapi', name: 'CLIProxyAPI', repository: 'router-for-me/CLIProxyAPI', current_version: 'v8.0.53', current_commit: 'a'.repeat(40), latest_version: 'v8.0.54', latest_commit: 'c'.repeat(40), state: 'update_available', stale: false },
    { id: 'cpa-manager-plus', name: 'CPA-Manager-Plus', repository: 'seakee/CPA-Manager-Plus', current_version: 'v1.14.10', current_commit: 'b'.repeat(40), latest_version: 'v1.14.10', latest_commit: 'b'.repeat(40), state: 'up_to_date', stale: false },
  ] };
};
describe('Direct upstream source updates', () => {
  it('shows both fixed upstreams without requesting a release index service', async () => {
    directSources();
    await act(async () => { renderer = create(<ProductReleasePage />); });
    expect(content()).toContain('No release index or extra service is needed');
    expect(content()).toContain('v8.0.54'); expect(content()).toContain('v1.14.10');
    expect(content()).not.toContain('No product release index is configured');
    expect(button('Update CLIProxyAPI').props.disabled).toBe(false);
    expect(button('Update CPA-Manager-Plus').props.disabled).toBe(true);
    await act(async () => { button('Check upstream updates').props.onClick(); });
    expect(mocks.request).toHaveBeenCalledWith(expect.objectContaining({ url: 'https://local.example/plus-api/releases/upstreams/check', method: 'POST' }));
  });
  it('requires exact version confirmation and source-code trust before selecting one upstream', async () => {
    directSources();
    await act(async () => { renderer = create(<ProductReleasePage />); });
    await act(async () => { button('Update CLIProxyAPI').props.onClick(); });
    expect(button('Confirm build and upgrade').props.disabled).toBe(true);
    const inputs = renderer!.root.findAllByType('input');
    await act(async () => { inputs[0].props.onChange({ target: { value: 'CLIProxyAPI v8.0.54' } }); });
    expect(button('Confirm build and upgrade').props.disabled).toBe(true);
    await act(async () => { inputs[1].props.onChange({ target: { checked: true } }); });
    await act(async () => { button('Confirm build and upgrade').props.onClick(); });
    expect(mocks.request).toHaveBeenCalledWith(expect.objectContaining({ url: 'https://local.example/plus-api/releases/upstreams/install', method: 'POST', data: { project: 'cliproxyapi', targets: { cliproxyapi: 'c'.repeat(40) }, confirmation: 'CLIProxyAPI v8.0.54', trust_source_code: true } }));
  });
  it('pins both identities when both projects are selected', async () => {
    directSources();
    await act(async () => { renderer = create(<ProductReleasePage />); });
    await act(async () => { button('Update both projects').props.onClick(); });
    const inputs = renderer!.root.findAllByType('input');
    await act(async () => { inputs[0].props.onChange({ target: { value: 'CLIProxyAPI v8.0.54 + CPA-Manager-Plus v1.14.10' } }); inputs[1].props.onChange({ target: { checked: true } }); });
    await act(async () => { button('Confirm build and upgrade').props.onClick(); });
    expect(mocks.request).toHaveBeenCalledWith(expect.objectContaining({ data: expect.objectContaining({ project: 'all', targets: { cliproxyapi: 'c'.repeat(40), 'cpa-manager-plus': 'b'.repeat(40) } }) }));
  });
  it('shows a fresh upstream reminder even without a product release index', async () => {
    directSources();
    await act(async () => { renderer = create(<ProductUpdateNotice />); });
    expect(content()).toContain('Upstream updates available: CLIProxyAPI v8.0.54');
  });
});


const runningOperation = (step = 'product_tests') => ({
  id: 'active-source-operation', kind: 'install', state: 'validating_source', from_version: '0.4.0-rc.2', target_version: '0.4.0-rc.2',
  created_at: '2026-11-04T15:15:00Z', updated_at: '2026-11-04T15:16:00Z', rollback_available: false,
  progress: { step, completed: step === 'binary_build' ? 7 : 6, total: 17, step_started_at: '2026-11-04T15:15:30Z', last_activity_at: '2026-11-04T15:16:00Z' },
});
describe('Visible source-build progress and safe diagnosis', () => {
  it('puts actual build stages above source actions, not below the page', async () => {
    directSources(); operations = { ...operations, busy: true, history: [runningOperation()] };
    await act(async () => { renderer = create(<ProductReleasePage />); });
    expect(content().indexOf('Upgrade progress and diagnostics')).toBeLessThan(content().indexOf('Update the two upstream projects directly'));
    expect(content()).toContain('Fetching Go dependencies and testing the product');
    const progress = renderer!.root.findByType('progress');
    expect(progress.props.value).toBe(6); expect(progress.props.max).toBe(17);
    expect(content()).toContain('not a time estimate');
    expect(button('Update CLIProxyAPI').props.disabled).toBe(true);
  });
  it('keeps operation progress visible when an unrelated metadata request fails', async () => {
    directSources(); operations = { ...operations, busy: true, history: [runningOperation()] };
    mocks.get.mockRejectedValueOnce(new Error('version metadata offline'));
    await act(async () => { renderer = create(<ProductReleasePage />); });
    expect(content()).toContain('Fetching Go dependencies and testing the product');
    expect(content()).toContain('version metadata offline');
  });
  it('does not let a delayed older response move progress backwards', async () => {
    directSources(); const previous = { ...operations, busy: true, history: [runningOperation()] };
    const original = mocks.request.getMockImplementation()!;
    let resolveOld!: (value: unknown) => void;
    let operationRequests = 0;
    mocks.request.mockImplementation((request: { url: string; method: string }) => {
      if (request.url.endsWith('/operations') && operationRequests++ === 0) return new Promise((resolve) => { resolveOld = resolve; });
      return original(request);
    });
    await act(async () => { renderer = create(<ProductReleasePage />); });
    operations = { ...operations, busy: true, history: [runningOperation('binary_build')] };
    await act(async () => { button('Check releases').props.onClick(); });
    expect(content()).toContain('Compiling the integrated application');
    await act(async () => { resolveOld({ data: previous }); });
    expect(content()).toContain('Compiling the integrated application');
  });
  it('shows an actionable network cause without raw secrets or an automatic retry', async () => {
    directSources(); operations = { ...operations, history: [{ ...runningOperation(), state: 'failed', failure_code: 'go_dependency_network', error: 'private-password private-config-path' }] };
    await act(async () => { renderer = create(<ProductReleasePage />); });
    expect(content()).toContain('GOPROXY'); expect(content()).toContain('retry manually');
    expect(content()).not.toContain('private-password'); expect(content()).not.toContain('private-config-path');
    expect(mocks.request).not.toHaveBeenCalledWith(expect.objectContaining({ method: 'POST' }));
  });
  it('formats elapsed time honestly, including old records without timestamps', () => {
    expect(duration('2026-11-04T15:15:00Z', Date.parse('2026-11-04T15:16:05Z'))).toBe('1:05');
    expect(duration(undefined, Date.now())).toBe('—');
  });
});

it('ignores old connection responses after the management connection changes', async () => {
  directSources(); const previous = { ...operations, busy: true, history: [{ ...runningOperation(), id: 'previous-connection-operation' }] };
  const original = mocks.request.getMockImplementation()!;
  let resolveOld!: (value: unknown) => void;
  mocks.request.mockImplementation((request: { url: string; method: string }) => {
    if (request.url === 'https://local.example/plus-api/releases/operations') return new Promise((resolve) => { resolveOld = resolve; });
    return original(request);
  });
  await act(async () => { renderer = create(<ProductReleasePage />); });
  mocks.auth.apiBase = 'https://other.example/v0/management'; mocks.auth.managementKey = 'other-admin-fixture';
  operations = { ...operations, busy: true, history: [{ ...runningOperation('binary_build'), id: 'new-connection-operation' }] };
  await act(async () => { renderer!.update(<ProductReleasePage />); });
  expect(content()).toContain('new-connection-operation');
  await act(async () => { resolveOld({ data: previous }); });
  expect(content()).toContain('new-connection-operation');
  expect(content()).not.toContain('previous-connection-operation');
});


it.each(['resolve', 'reject'])('does not let a stale %s from connection A clear B action state', async (settlement) => {
  available();
  const original = mocks.request.getMockImplementation()!;
  let resolveA!: (value: unknown) => void;
  let rejectA!: (reason: unknown) => void;
  let resolveB!: (value: unknown) => void;
  mocks.request.mockImplementation((request: { url: string; method: string }) => {
    if (request.url === 'https://local.example/plus-api/releases/operations/install') return new Promise((resolve, reject) => { resolveA = resolve; rejectA = reject; });
    if (request.url === 'https://other.example/plus-api/releases/operations/install') return new Promise((resolve) => { resolveB = resolve; });
    return original(request);
  });
  await act(async () => { renderer = create(<ProductReleasePage />); });
  await act(async () => { button('Upgrade and restart').props.onClick(); });
  await act(async () => { renderer!.root.findByType('input').props.onChange({ target: { value: '0.3.0-rc.2' } }); });
  await act(async () => { button('Confirm upgrade and restart').props.onClick(); });
  mocks.auth.apiBase = 'https://other.example/v0/management'; mocks.auth.managementKey = 'other-admin-fixture';
  await act(async () => { renderer!.update(<ProductReleasePage />); });
  await act(async () => { button('Upgrade and restart').props.onClick(); });
  await act(async () => { renderer!.root.findByType('input').props.onChange({ target: { value: '0.3.0-rc.2' } }); });
  await act(async () => { button('Confirm upgrade and restart').props.onClick(); });
  expect(button('Confirm upgrade and restart').props.disabled).toBe(true);
  await act(async () => {
    if (settlement === 'resolve') resolveA({ data: { id: 'old-connection-operation', state: 'queued' } });
    else rejectA(new Error('stale A private failure'));
  });
  expect(button('Confirm upgrade and restart').props.disabled).toBe(true);
  expect(content()).not.toContain('stale A private failure');
  expect(content()).not.toContain('Connection changed');
  await act(async () => { resolveB({ data: { id: 'new-connection-operation', state: 'queued' } }); });
  expect(content()).toContain('new-connection-operation');
  expect(content()).not.toContain('old-connection-operation');
});

it('blocks duplicate source actions immediately after acceptance even before history catches up', async () => {
  directSources();
  await act(async () => { renderer = create(<ProductReleasePage />); });
  await act(async () => { button('Update CLIProxyAPI').props.onClick(); });
  const inputs = renderer!.root.findAllByType('input');
  await act(async () => { inputs[0].props.onChange({ target: { value: 'CLIProxyAPI v8.0.54' } }); inputs[1].props.onChange({ target: { checked: true } }); });
  await act(async () => { button('Confirm build and upgrade').props.onClick(); });
  expect(content()).toContain('source-operation');
  expect(button('Update CLIProxyAPI').props.disabled).toBe(true);
  expect(button('Update both projects').props.disabled).toBe(true);
});
