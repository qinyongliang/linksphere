import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
const read=path=>readFile(new URL('../'+path,import.meta.url),'utf8');
test('English landing and brief distinguish local evidence from real communications',async()=>{
 for(const path of ['site/index.html','site/brief.en.md']){
  const text=await read(path);assert.match(text,/integration candidate/);assert.match(text,/mock\/race-test coverage/);assert.match(text,/communications MCP transport are not connected/);assert.match(text,/real-device calls, audio, SMS and listeners have not been accepted/);
 }
});
test('Chinese landing and brief carry the same unaccepted communications boundary',async()=>{
 for(const path of ['site/index.zh-CN.html','site/brief.zh-CN.md']){
  const text=await read(path);assert.match(text,/模拟／竞态测试/);assert.match(text,/通信 MCP 传输尚未接通/);assert.match(text,/真实设备通话、音频、短信及监听尚未验收/);
 }
});
test('current call documentation distinguishes owned local events from absent transport',async()=>{
 const text=await read('docs/android-call-core.md');assert.match(text,/Cancel signals cancellation/);assert.match(text,/Legacy StartMedia retains its Background/);assert.match(text,/bounded, authorized local event buffer/);assert.match(text,/no production\nnetwork event subscription/);assert.match(text,/third_party\/purego/);assert.match(text,/third_party\/anet/);assert.match(text,/contains no SMS implementation/);
 assert.doesNotMatch(text,/Cancel performs an actual hangup|bounded event delivery still need adapter/);
});
