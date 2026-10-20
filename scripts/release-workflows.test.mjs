import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
const read=path=>readFile(new URL('../'+path,import.meta.url),'utf8');
test('Windows installer is per-user and retains application data',async()=>{
 const source=await read('client/wails/installer/windows.iss');
 assert.match(source,/PrivilegesRequired=lowest/);
 assert.ok(source.includes('DefaultDirName={localappdata}\\Programs\\LinkSphere'));
 assert.ok(source.includes('LinkSphere-Setup-{#ReleaseVersion}-windows-amd64'));
 assert.doesNotMatch(source,/UninstallDelete/);
});
test('Windows workflow creates checksummed installer and portable archives',async()=>{
 const source=await read('.github/workflows/client-installer.yml');
 for(const required of ['windows-latest','check-public-content.mjs','Get-FileHash','SHA256','Inno Setup','contents: write','-portable.zip']) assert.ok(source.includes(required),required);
 const build=await read('build-client-windows.ps1');assert.ok(build.includes('-trimpath'));assert.ok(build.includes('StartsWith($packageBase'));
});
test('Formal Android packages require maintained signing configuration',async()=>{
 const source=await read('.github/workflows/android-companion.yml');
 for(const required of ['needs: [installers, signing-config]',"needs.signing-config.outputs.available == 'true'",'no signing identity is replaced','Sign APK using the maintained release certificate'])assert.ok(source.includes(required),required);
});
test('Manual desktop packages use the requested version',async()=>{
 const source=await read('.github/workflows/desktop.yml');
 assert.equal((source.match(/^\s+REQUESTED_VERSION:/gm)||[]).length,2);
 assert.equal(source.split('version="'+ '$'+'{REQUESTED_VERSION:-development}"').length-1,2);
});
test('Pages deploys using a preconfigured workflow site',async()=>{
 const source=await read('.github/workflows/pages.yml');assert.ok(source.includes('actions/configure-pages@'));assert.ok(!source.includes('enablement: true'));assert.ok(source.includes('actions/deploy-pages@'));
});

test('Manual repair builds publish to the requested semantic version, not the branch name',async()=>{
 const source=await read('.github/workflows/release.yml');
 assert.ok(source.includes('Publish validated packages to the requested release'));
 assert.ok(source.includes('RELEASE_VERSION: ${{ inputs.version || github.ref_name }}'));
 assert.ok(source.includes('tag_name: ${{ inputs.version || github.event.release.tag_name || github.ref_name }}'));
 assert.ok(source.includes('Published versions require a semantic version tag'));
});

test('Repair publication reuses validated archives without rewriting release metadata',async()=>{
 const workflow=await read('.github/workflows/publish-archives.yml');
 const script=await read('scripts/upload-release-assets.sh');
 assert.ok(workflow.includes('Require successful validation and package build steps'));
 assert.ok(workflow.includes('actions/download-artifact@v4'));
 assert.ok(script.includes('sha256sum --check checksums.txt'));
 assert.ok(script.includes('gh release upload'));
 assert.ok(!script.includes('--clobber'));
 assert.ok(!script.includes('gh release edit'));
});
