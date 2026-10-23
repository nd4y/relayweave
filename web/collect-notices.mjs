import {existsSync,mkdirSync,readFileSync,readdirSync,writeFileSync} from 'node:fs';
import {join} from 'node:path';

// Preserve the actual upstream notices of installed production dependencies.
const lock=JSON.parse(readFileSync('package-lock.json','utf8'));
const notices=[],missing=[];
for(const [directory,entry] of Object.entries(lock.packages).sort(([a],[b])=>a.localeCompare(b))){
 if(!directory||entry.dev||!existsSync(join(directory,'package.json')))continue;
 const pkg=JSON.parse(readFileSync(join(directory,'package.json'),'utf8'));
 let noticeDirectory=directory;
 let files=readdirSync(directory,{withFileTypes:true}).filter(f=>f.isFile()&&/^(licen[cs]e|notice|copying|copyright)([._-].*)?$/i.test(f.name)).map(f=>f.name).sort();
 // This exact MUI X release declares MIT in its source/npm manifest but omits
 // a notice file. Preserve the same repository's Community MIT notice, with
 // explicit provenance rather than inventing an upstream copyright statement.
 let provenance='';
 if(!files.length&&pkg.name==='@mui/x-virtualizer'&&pkg.version==='0.4.2'&&pkg.license==='MIT'&&pkg.repository?.url==='git+https://github.com/mui/mui-x.git'){
  noticeDirectory='node_modules/@mui/x-data-grid';files=['LICENSE'];
  provenance='Notice source: @mui/x-data-grid/LICENSE from MUI X v8.29.5; virtualizer manifest v0.4.2 declares MIT.\n';
 }
 if(!files.length){missing.push(`${pkg.name}@${pkg.version}`);continue;}
 notices.push(`Package: ${pkg.name}@${pkg.version}\nDeclared license: ${pkg.license||'See upstream text'}\n${provenance}`);
 for(const file of files)notices.push(`--- ${noticeDirectory}/${file} ---\n${readFileSync(join(noticeDirectory,file),'utf8')}\n`);
}
if(missing.length)throw new Error(`Missing upstream notice files: ${missing.join(', ')}`);
mkdirSync('public',{recursive:true});
writeFileSync('public/third-party-notices.txt','Third-party components included in the frontend. These upstream terms do not license original RelayWeave source.\n\n'+notices.join('\n'));
console.log(`Preserved notices for ${notices.filter(n=>n.startsWith('Package:')).length} installed production dependencies`);
