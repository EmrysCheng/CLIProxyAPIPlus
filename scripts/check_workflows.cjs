// Validate YAML structure with the already-locked frontend YAML dependency.
const fs = require('fs'), path = require('path');
const root = path.resolve(__dirname, '..');
const yaml = require(path.join(path.resolve(process.argv[2]), 'node_modules/yaml'));
for (const file of fs.readdirSync(path.join(root, '.github/workflows'))) {
  const d = yaml.parse(fs.readFileSync(path.join(root, '.github/workflows', file), 'utf8'));
  if (!d.on || !d.jobs || d.permissions?.contents !== 'read' || Object.entries(d.permissions).some(([key, value]) => value !== 'read' || !['contents', 'actions'].includes(key))) throw new Error('Invalid/overprivileged workflow ' + file);
  const release = file === 'portable-release.yml';
  if (release && (Object.keys(d.on).join(',') !== 'workflow_dispatch' || d.concurrency?.['cancel-in-progress'] !== false)) throw new Error('Portable publication must be explicit and serialized');
  for (const [name, job] of Object.entries(d.jobs)) {
    if (!Array.isArray(job.steps) || !job.steps.length) throw new Error('Missing steps: ' + name);
    const publisher = release && name === 'publish';
    if (publisher) {
      if (job.permissions?.contents !== 'write' || job.permissions?.actions !== 'read' || Object.keys(job.permissions).length !== 2 || JSON.stringify(job.needs) !== JSON.stringify(['preflight', 'build']) || job.if !== "github.ref == 'refs/heads/master' && github.repository == 'EmrysCheng/CLIProxyAPIPlus'") throw new Error('Invalid release publisher trust boundary');
      if (job.steps.some(step => /project\.py|npm |go test|smoke_/.test(step.run || ''))) throw new Error('Do not execute upstream/package code with release write token');
    } else if (job.permissions && Object.values(job.permissions).some(value => value !== 'read')) throw new Error('Only explicit release publisher may write');
    for (const step of job.steps) {
      if (step.uses && !/^[\w/-]+@[a-f0-9]{40}$/.test(step.uses)) throw new Error('Unpinned Action ' + step.uses);
      if (step.uses?.startsWith('actions/checkout@') && step.with?.['persist-credentials'] !== false) throw new Error('Checkout must not persist credentials');
    }
  }
  console.log('Validated workflow:', file);
}
const compose = yaml.parse(fs.readFileSync(path.join(root, 'compose.yaml'), 'utf8'));
if (Object.keys(compose.services).length !== 1) throw new Error('Expected one integrated container');
console.log('Validated single-service Compose YAML');
