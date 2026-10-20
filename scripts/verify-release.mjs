import {createHash} from 'node:crypto';

const repository=process.env.GITHUB_REPOSITORY;
const tag=process.env.RELEASE_TAG;
if(!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository||''))throw new Error('Set GITHUB_REPOSITORY');
if(!/^v\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?$/.test(tag||''))throw new Error('Set a semantic RELEASE_TAG');
const headers={'Accept':'application/vnd.github+json','User-Agent':'LinkSphere-Release-Verification'};
if(process.env.GITHUB_TOKEN)headers.Authorization='Bearer '+process.env.GITHUB_TOKEN;
const metadata=await fetch(`https://api.github.com/repos/${repository}/releases/tags/${tag}`,{headers,signal:AbortSignal.timeout(30000)});
if(!metadata.ok)throw new Error('Release metadata unavailable: '+metadata.status);
const release=await metadata.json();
const assets=new Map(release.assets.map(asset=>[asset.name,asset]));
const required=[`LinkSphere-Setup-${tag}-windows-amd64.exe`,`LinkSphere-${tag}-windows-amd64-portable.zip`,...['linux-amd64','linux-arm64','darwin-amd64','darwin-arm64'].map(platform=>`gosshd-${tag}-${platform}.tar.gz`),`gosshd-${tag}-windows-amd64.zip`];
for(const name of required)if(!assets.has(name))throw new Error('Missing required package: '+name);
const expected=new Map();
for(const asset of assets.values()){
 if(!/(?:checksums.*\.txt|\.sha256)$/.test(asset.name))continue;
 const response=await fetch(asset.browser_download_url,{signal:AbortSignal.timeout(60000)});
 if(!response.ok)throw new Error('Checksum download failed: '+asset.name+' HTTP '+response.status);
 for(const line of (await response.text()).split(/\r?\n/)){
  const match=/^([0-9a-f]{64})\s+\*?(.+)$/i.exec(line.trim());
  if(!match)continue;
  const name=match[2];const checksum=match[1].toLowerCase();
  if(expected.has(name)&&expected.get(name)!==checksum)throw new Error('Conflicting checksums: '+name);
  expected.set(name,checksum);
 }
}
let verified=0;
for(const asset of assets.values()){
 if(!/\.(?:exe|zip|tar\.gz|apk)$/.test(asset.name))continue;
 const checksum=expected.get(asset.name);
 if(!checksum)throw new Error('Package has no published SHA256: '+asset.name);
 const response=await fetch(asset.browser_download_url,{signal:AbortSignal.timeout(180000)});
 if(!response.ok||!response.body)throw new Error('Package download failed: '+asset.name+' HTTP '+response.status);
 const digest=createHash('sha256');let bytes=0;let prefix=Buffer.alloc(0);
 for await(const chunk of response.body){digest.update(chunk);bytes+=chunk.length;if(prefix.length<4)prefix=Buffer.concat([prefix,Buffer.from(chunk)]).subarray(0,4);}
 const actual=digest.digest('hex');
 if(actual!==checksum)throw new Error('SHA256 mismatch: '+asset.name);
 if(bytes!==asset.size)throw new Error('Download length mismatch: '+asset.name);
 if(asset.name.endsWith('.exe')&&prefix.subarray(0,2).toString()!=='MZ')throw new Error('Invalid executable: '+asset.name);
 if(/\.(zip|apk)$/.test(asset.name)&&prefix.subarray(0,2).toString()!=='PK')throw new Error('Invalid ZIP package: '+asset.name);
 if(asset.name.endsWith('.tar.gz')&&(prefix[0]!==0x1f||prefix[1]!==0x8b))throw new Error('Invalid gzip archive: '+asset.name);
 console.log(`Verified ${asset.name} (${bytes} bytes)`);verified++;
}
console.log(`Verified ${verified} publicly downloadable packages for ${tag}.`);
