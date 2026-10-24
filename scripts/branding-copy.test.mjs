import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
const read = p => readFile(new URL('../'+p, import.meta.url), 'utf8');
test('visible service and setup copy uses LinkSphere', async()=>{
  const agent = await read('cmd/gosshd-agent/main.go');
  assert.match(agent,/public LinkSphere server URL/);
  const install = await read('internal/server/api_agents.go');
  assert.match(install,/Description=LinkSphere Agent/);
  assert.match(install,/-DisplayName "LinkSphere Agent"/);
  assert.doesNotMatch(install,/gosshd bastion agent/);
  assert.doesNotMatch(await read('internal/server/app.go'),/gosshd-server ready/);
  assert.match(await read('scripts/android-desktop/accessibility.sh'),/In LinkSphere Agent:/);
  assert.doesNotMatch(await read('docs/android-accessibility.md'),/GOSSHD Agent/);
});
test('branding retains existing service, environment and Android identity contracts',async()=>{
  assert.match(await read('internal/server/api_agents.go'),/ExecStart=\/usr\/local\/bin\/gosshd-agent/);
  assert.match(await read('scripts/android-desktop/accessibility.sh'),/export GOSSHD_ANDROID_BACKEND=accessibility/);
  assert.match(await read('client/android-desktop/src/main/AndroidManifest.xml'),/package="com.gosshd.desktop"/);
  assert.match(await read('docs/branding-compatibility.md'),/aliases/);
});

test('macOS visible name changes without changing permission or bundle identity',async()=>{
  const script=await read('scripts/build-desktop-macos.sh');
  assert.match(script,/<key>CFBundleName<\/key><string>LinkSphere Desktop Helper<\/string>/);
  assert.match(script,/com\.gosshd\.desktop-helper/);
  assert.match(script,/<key>CFBundleExecutable<\/key><string>gosshd-desktop-helper<\/string>/);
  const docs=await read('docs/desktop-mcp.md');
  assert.match(docs,/remote LinkSphere Agent/);
  assert.match(docs,/GOSSHD Desktop Helper\.app\/Contents\/MacOS\/gosshd-desktop-helper/);
});

test('installer status copy uses LinkSphere without renaming the executable',async()=>{
  const script=await read('internal/server/install_android.sh');
  assert.doesNotMatch(script,/echo ["']gosshd-agent (?:already|running|stopped|failed)/);
  assert.match(script,/"\$dir\/gosshd-agent"/);
});
