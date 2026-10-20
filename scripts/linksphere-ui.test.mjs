import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { mkdir, readFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import test from 'node:test';
test('LinkSphere bilingual console, node creation, routes and mobile navigation', {
 skip: !process.env.GOSSHD_UI_E2E_PLAYWRIGHT || !process.env.GOSSHD_UI_E2E_BROWSER,
 timeout: 120000,
}, async () => {
const require = createRequire(import.meta.url);
const { chromium } = require(process.env.GOSSHD_UI_E2E_PLAYWRIGHT);
const root = fileURLToPath(new URL('../', import.meta.url));
const dist = path.join(root, 'web/dist');
const out = path.join(root, 'build/linksphere-verification') + path.sep;
await mkdir(out, {recursive:true});
const server = createServer(async (request,response) => {
 try {
  const pathname = decodeURIComponent(new URL(request.url,'http://localhost').pathname);
  const file = pathname.startsWith('/assets/') ? path.resolve(dist, '.'+pathname) : path.join(dist,'index.html');
  if (!file.startsWith(dist+path.sep)) {response.writeHead(403);response.end();return;}
  const types={'.html':'text/html','.js':'application/javascript','.css':'text/css','.png':'image/png','.ttf':'font/ttf'};
  response.writeHead(200,{'Content-Type':types[path.extname(file)] || 'application/octet-stream'});
  response.end(await readFile(file));
 } catch {response.writeHead(404);response.end();}
});
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
const base = 'http://127.0.0.1:'+server.address().port;
let browser;
try {
 browser = await chromium.launch({ executablePath: process.env.GOSSHD_UI_E2E_BROWSER, headless: true });
 for (const locale of ['zh-CN','en']) {
  const context = await browser.newContext({ viewport: { width: 1366, height: 900 }, locale });
  await context.addInitScript(({locale}) => { localStorage.setItem('gosshd_locale',locale);localStorage.setItem('gosshd_theme','light'); }, {locale});
  const page = await context.newPage();
  const errors=[]; page.on('pageerror',e=>errors.push(e.message));
  let enrollment;
  const branding={app_name:'LinkSphere',app_description:'Let AI manage all your devices'};
  await page.route('**/api/**',async route=>{
   const req=route.request(), path=new URL(req.url()).pathname;
   let data={keys:[],targets:[],folders:[],credentials:[],members:[],groups:[],policies:[],configs:[],prompts:[],tokens:[],logs:[],reviews:[],tunnels:[],total:0,page:1,page_size:20};
   if(path==='/api/auth/providers')data={branding,registration_enabled:true};
   if(path==='/api/me')data={user:{id:'demo-user',email:'demo@example.com',display_name:'Demo',is_system_admin:true,auth_provider:'local'},organizations:[{id:'demo-org',name:'Demo Workspace',slug:'demo',is_personal:false,role:'owner'}],runtime:{...branding,ssh_host:'linksphere.example.com',ssh_port:22022}};
   if(path==='/api/agent-enrollments'&&req.method()==='POST') {enrollment=req.postDataJSON(); return route.fulfill({json:{id:'demo-enrollment',server_url:'https://linksphere.example.com',install_cmd:'echo demo'}});}
   if(path==='/api/me/settings')data={connect_open_mode:'popup',connect_attach_existing:false};
   if(path==='/api/manual-review-choice')data={auto_allow_target_ids:[],auto_allow_minutes:0};
   if(path==='/api/admin/settings')data={branding,forward_log_retention_days:7};
   await route.fulfill({json:data});
  });
  await page.goto(base+'/targets');
  await page.locator('.brand-row strong').filter({hasText:'LinkSphere'}).waitFor();
  assert.equal(await page.locator('.nav-group').count(),3);
  assert.equal(await page.locator('.resource-head > div:not(.resource-actions)').count(),0);
  assert.equal(await page.locator('.topbar h1').textContent(),locale==='zh-CN'?'服务':'Services');
  await page.screenshot({path:out+`console-${locale}.png`,fullPage:true});
  await page.getByRole('button',{name:locale==='zh-CN'?'添加服务':'Add service',exact:true}).first().click();
  assert.equal(await page.getByRole('tab',{name:locale==='zh-CN'?'节点':'Node',exact:true}).getAttribute('aria-selected'),'true');
  assert.equal(await page.locator('input[name="alias"]').count(),1);
  assert.equal(await page.locator('input[name="host"]').count(),0);
  assert.ok(!(await page.locator('body').innerText()).includes('只需要填写别名'));
  await page.screenshot({path:out+`add-node-${locale}.png`,fullPage:true});
  await page.getByRole('tab',{name:locale==='zh-CN'?'直连服务':'Direct connection',exact:true}).click();
  await page.locator('input[name="name"]').fill('Demo service');
  await page.locator('input[name="alias"]').fill('demo-service');
  await page.getByRole('button',{name:locale==='zh-CN'?'下一步':'Next',exact:true}).click();
  await page.locator('input[name="host"]').waitFor();
  await page.getByRole('tab',{name:locale==='zh-CN'?'节点':'Node',exact:true}).click();
  await page.locator('input[name="alias"]').fill('demo-device');
  await page.getByRole('button',{name:locale==='zh-CN'?'创建安装令牌':'Create install token',exact:true}).click();
  await page.locator('.ant-drawer-title strong').filter({hasText:locale==='zh-CN'?'安装引导':'Install guide'}).waitFor();
  assert.equal(enrollment.label,'demo-device');
  assert.equal(enrollment.owner_id,'demo-org');
  for (const path of ['/orgs','/org-admin','/keys','/policies','/audit','/system-admin','/tunnels']) {
   await page.goto(base+path);
   await page.locator('.topbar h1').waitFor({state:'attached'});
   assert.equal(await page.locator('.resource-head > div:not(.resource-actions)').count(),0,path);
   assert.ok(!/堡垒机|SSH 服务|添加直连服务器/.test(await page.locator('body').innerText()),path);
  }
  await page.setViewportSize({width:390,height:844});
  await page.goto(base+'/targets');
  await page.locator('.topbar h1').waitFor({state:'attached'});
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'mobile overflow');
  await page.locator('.mobile-menu-button').click();
  await page.locator('.side-nav a[href="/keys"]').click();
  await page.locator('.topbar h1').filter({hasText:locale==='zh-CN'?'AI 接入与凭证':'AI & credentials'}).waitFor({state:'attached'});
  await page.screenshot({path:out+`mobile-${locale}.png`,fullPage:true});
  assert.deepEqual(errors,[]);
  await context.close();
  console.log(locale+': console routes, node default/enrollment, direct wizard and mobile navigation passed');
 }
} finally {
 if (browser) await browser.close();
 await new Promise(resolve=>server.close(resolve));
}
});
