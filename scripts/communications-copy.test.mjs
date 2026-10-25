import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
const read=path=>readFile(new URL('../'+path,import.meta.url),'utf8');
// Validate published capability/conditions copy, not superseded development-status wording.
const contracts = [
 {paths:['site/index.html','site/brief.en.md'], requirements:[
  /plugin architecture for Android root devices/,
  /calls, audio, SMS and events/,
  /device capabilities and explicit permissions/,
  /Communication features require a trusted root service/,
  /Remote communication and its MCP tools are not yet available in this version/,
  /device support requires validation after integration/,
 ]},
 {paths:['site/index.zh-CN.html','site/brief.zh-CN.md'], requirements:[
  /面向 Android root 设备的通信插件架构/,
  /通话、音频、短信与事件/,
  /设备能力和明确授权/,
  /通信能力需由受信任的 root 服务提供/,
  /当前版本尚未开放远程通信与对应 MCP 工具/,
  /具体设备支持以完成接入后的验证结果为准/,
 ]},
];
function requireBoundary(text, requirements) {
 for(const requirement of requirements) assert.match(text, requirement);
}
for(const {paths,requirements} of contracts) for(const path of paths) {
 test(`${path} retains every communication access and availability condition`,async()=>{
  requireBoundary(await read(path),requirements);
 });
 test(`${path} cannot pass if any communication condition is removed`,async()=>{
  const text=await read(path);
  for(const requirement of requirements) {
   assert.match(text,requirement);
   assert.throws(()=>requireBoundary(text.replaceAll(new RegExp(requirement.source,'g'),''),requirements),assert.AssertionError);
  }
 });
}
test('landing visuals explicitly remain illustrative rather than live device evidence',async()=>{
 assert.match(await read('site/index.html'),/Visuals and animations illustrate workflows, not a live connection/);
 assert.match(await read('site/index.zh-CN.html'),/画面与动画为工作流示意，非实时连接/);
});
test('current call documentation distinguishes owned local events from absent transport',async()=>{
 const text=await read('docs/android-call-core.md');assert.match(text,/Cancel signals cancellation/);assert.match(text,/Legacy StartMedia retains its Background/);assert.match(text,/bounded, authorized local event buffer/);assert.match(text,/no production\nnetwork event subscription/);assert.match(text,/third_party\/purego/);assert.match(text,/third_party\/anet/);assert.match(text,/contains no SMS implementation/);
 assert.doesNotMatch(text,/Cancel performs an actual hangup|bounded event delivery still need adapter/);
});
