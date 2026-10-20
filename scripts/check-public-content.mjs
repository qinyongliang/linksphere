import { execFileSync } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';
import { isIP } from 'node:net';

const staged = process.argv.includes('--staged');
const git = (args, options = {}) => execFileSync('git', args, { maxBuffer: 64 * 1024 * 1024, ...options });
const records = git(['ls-files', '--stage', '-z'], {encoding:'utf8'}).split('\0').filter(Boolean).map(record => {
  const match = /^(\d+) ([0-9a-f]+) (\d+)\t(.+)$/.exec(record);
  if (!match || match[3] !== '0') throw new Error('Resolve index conflicts before checking public content.');
  return {mode:match[1], oid:match[2], path:match[4]};
});
const entries = staged ? records : records.filter(record => existsSync(record.path));
let objects;
if (staged && entries.length) {
  const output = git(['cat-file', '--batch'], {input:entries.map(entry=>entry.oid).join('\n')+'\n'});
  let cursor=0;
  objects=entries.map(() => {
    const end=output.indexOf(10,cursor);
    const header=output.subarray(cursor,end).toString('ascii');
    const match=/^[0-9a-f]+ blob (\d+)$/.exec(header);
    if (!match) throw new Error('Unsupported indexed object: '+header);
    const start=end+1, size=Number(match[1]);
    cursor=start+size+1;
    return output.subarray(start,start+size);
  });
}
const privatePaths = [
  /^(?:\.learnings|\.superpowers|local-only|design|data|agent-cache)\//,
  /^docs\/superpowers\//, /^docs\/.*-deploy(?:ment)?.*\.md$/,
  /^docs\/http-forward-load-results\.md$/, /^\.github\/workflows\/deploy-arm64\.yml$/,
  /^web\/ai-bastion-design-board\.html$/, /^scripts\/deploy_release\.py$/,
  /(?:^|\/)\.env(?:$|\.(?!example$|sample$))/, /\.(?:keystore|jks|p12|pfx|db|sqlite|sqlite3|log|cast)$/i,
];
const secrets = /-----BEGIN (?:RSA |EC |OPENSSH |ENCRYPTED )?PRIVATE KEY-----|\bgh[pousr]_[A-Za-z0-9]{30,}\b|\bgithub_pat_[A-Za-z0-9_]{40,}\b|\bAKIA[0-9A-Z]{16}\b|\bsk-[A-Za-z0-9]{32,}\b/;
const personalPath = /\b[A-Za-z]:[\\/](?:Users|projects|workspaces?)[\\/](?!(?:user|example|public|default|<[^>]+>)(?:[\\/]|["'\s]|$))[^\s"']+|(?:^|[\s"'`])\/(?:Users|home)\/(?!(?:user|example|runner|shared|\$[A-Za-z_][A-Za-z0-9_]*|<[^>]+>)(?:\/|["'\s]|$))[^\s"']+/i;
const terms=(process.env.PUBLIC_CONTENT_FORBIDDEN_TERMS||'').split(',').map(value=>value.trim()).filter(Boolean);
let repository=process.env.PUBLIC_CONTENT_ALLOWED_REPOSITORY||'';
if (!repository) {
  try { repository=git(['remote','get-url','origin'],{encoding:'utf8'}).trim().match(/github\.com[:/]([^/]+\/[^/]+?)(?:\.git)?$/)?.[1]||''; } catch { /* A new unpublished repository may have no remote yet. */ }
}
const [owner,project]=repository.split('/');
const findings=[];
const report=(path,line,reason)=>findings.push(`${path}${line?':'+line:''}: ${reason}`);
for (let index=0;index<entries.length;index++) {
  const entry=entries[index];
  if (privatePaths.some(pattern=>pattern.test(entry.path))) report(entry.path,0,'local-only or sensitive file type');
  if (entry.mode==='160000') {report(entry.path,0,'embedded repository requires a separate publication review');continue;}
  const bytes=staged?objects[index]:readFileSync(entry.path);
  if (bytes.includes(0)) continue;
  const text=bytes.toString('utf8');
  const lines=text.split(/\r?\n/);
  for (let line=0;line<lines.length;line++) {
    let content=lines[line];
    if (secrets.test(content)) report(entry.path,line+1,'potential private key or access token');
    if (personalPath.test(content)) report(entry.path,line+1,'personal workspace path');
    if (repository) content=content.replaceAll(repository,'<repository>').replaceAll(`${owner}.github.io/${project}`,'<project-site>');
    for (const term of terms) {
      const escaped=term.replace(/[.*+?^${}()|[\]\\]/g,'\\$&');
      const pattern=/^[\w -]+$/.test(term)?new RegExp(`(?<![A-Za-z0-9_])${escaped}(?![A-Za-z0-9_])`,'i'):new RegExp(escaped,'i');
      if (pattern.test(content)) report(entry.path,line+1,'locally configured private identifier');
    }
    for (const match of content.matchAll(/(?<![\w.])(?:\d{1,3}\.){3}\d{1,3}(?![\w.])/g)) {
      const ip=match[0];
      if (isIP(ip)!==4)continue;
      if (!/^(?:0\.0\.0\.0$|127\.|192\.0\.2\.|198\.51\.100\.|203\.0\.113\.|198\.(?:18|19)\.)/.test(ip)) report(entry.path,line+1,'use a reserved example or benchmark address');
    }
  }
}
if (findings.length) {
  console.error('Public-content check failed:\n'+[...new Set(findings)].join('\n'));
  process.exit(1);
}
console.log(`Public-content checks passed for ${entries.length} ${staged?'staged':'working-tree'} files.`);
