import {cp,mkdir} from 'node:fs/promises';
import {fileURLToPath} from 'node:url';
const source=fileURLToPath(new URL('../client/wails/frontend/wailsjs/',import.meta.url));
const destination=fileURLToPath(new URL('../client/wails/frontend/dist/wailsjs/',import.meta.url));
await mkdir(destination,{recursive:true});
await cp(source,destination,{recursive:true,force:true});
console.log('Desktop client bindings prepared.');
