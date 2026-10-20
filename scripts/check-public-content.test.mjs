import test from 'node:test';
import assert from 'node:assert/strict';
import {execFileSync,spawnSync} from 'node:child_process';
import {mkdtempSync,writeFileSync,rmSync,mkdirSync} from 'node:fs';
import {tmpdir} from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
const checker=fileURLToPath(new URL('./check-public-content.mjs',import.meta.url));
function fixture(run){
 const dir=mkdtempSync(path.join(tmpdir(),'linksphere-public-check-'));
 try{
  execFileSync('git',['init','-q'],{cwd:dir});
  const stage=(name,text)=>{mkdirSync(path.dirname(path.join(dir,name)),{recursive:true});writeFileSync(path.join(dir,name),text);execFileSync('git',['add','--',name],{cwd:dir});};
  const check=(env={})=>spawnSync(process.execPath,[checker,'--staged'],{cwd:dir,encoding:'utf8',env:{...process.env,PUBLIC_CONTENT_FORBIDDEN_TERMS:'',...env}});
  run({dir,stage,check});
 }finally{rmSync(dir,{recursive:true,force:true});}
}
test('accepts source examples and public repository namespaces',()=>fixture(({stage,check})=>{
 stage('README.md','https://github.com/example-owner/linksphere\nhttps://example-owner.github.io/linksphere/\n192.0.2.1\n198.18.42.1');
 const result=check({PUBLIC_CONTENT_FORBIDDEN_TERMS:'example-owner',PUBLIC_CONTENT_ALLOWED_REPOSITORY:'example-owner/linksphere'});
 assert.equal(result.status,0,result.stderr);
}));
test('checks staged bytes even when the working copy has been cleaned',()=>fixture(({dir,stage,check})=>{
 stage('sample.txt','-----BEGIN '+'PRIVATE KEY-----');
 writeFileSync(path.join(dir,'sample.txt'),'clean working copy');
 const result=check();assert.equal(result.status,1);assert.match(result.stderr,/potential private key/);
}));
test('rejects local artifacts and configured personal identifiers',()=>fixture(({stage,check})=>{
 stage('local-only/notes.md','Example Personal Name');
 const result=check({PUBLIC_CONTENT_FORBIDDEN_TERMS:'Example Personal Name'});
 assert.equal(result.status,1);assert.match(result.stderr,/local-only/);assert.match(result.stderr,/private identifier/);
}));
test('rejects non-example addresses and personal machine paths',()=>fixture(({stage,check})=>{
 stage('fixture.txt',['10','24','8','9'].join('.')+'\n'+'C:'+'\\Users\\'+'sample-person'+'\\project');
 const result=check();assert.equal(result.status,1);assert.match(result.stderr,/reserved example/);assert.match(result.stderr,/workspace path/);
}));
