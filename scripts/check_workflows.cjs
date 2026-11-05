// Validate YAML structure with the already-locked frontend YAML dependency.
const fs=require('fs'),path=require('path');
const root=path.resolve(__dirname,'..');
const yaml=require(path.join(path.resolve(process.argv[2]),'node_modules/yaml'));
for(const file of fs.readdirSync(path.join(root,'.github/workflows'))){
 const d=yaml.parse(fs.readFileSync(path.join(root,'.github/workflows',file),'utf8'));
 if(!d.on||!d.jobs||d.permissions?.contents!=='read')throw new Error('Invalid/overprivileged workflow '+file);
 for(const [name,job] of Object.entries(d.jobs)){
  if(!Array.isArray(job.steps)||!job.steps.length)throw new Error('Missing steps: '+name);
  for(const step of job.steps)if(step.uses&&!/^[\w/-]+@[a-f0-9]{40}$/.test(step.uses))throw new Error('Unpinned Action '+step.uses);
 }
 console.log('Validated workflow:',file);
}
const compose=yaml.parse(fs.readFileSync(path.join(root,'compose.yaml'),'utf8'));
if(Object.keys(compose.services).length!==1)throw new Error('Expected one integrated container');
console.log('Validated single-service Compose YAML');
