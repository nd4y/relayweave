import { useState } from 'react';
import { Accordion, AccordionSummary, AccordionDetails, Alert, Box, Button, MenuItem, Stack, TextField, Typography } from '@mui/material';
import ExpandMoreRounded from '@mui/icons-material/ExpandMoreRounded';

export function CoreApproval({ approve }: { approve: (value: Record<string, unknown>) => Promise<unknown> }) {
  const [value, setValue] = useState({version:'',variant:'',repository:'XTLS/Xray-core',tag:'',arch:'amd64',url:'',sha256:'',license:'MPL-2.0'});
  const [busy,setBusy]=useState(false);
  const set=(key:string,text:string)=>setValue(v=>({...v,[key]:text}));
  const valid=/^\d{1,2}\.\d{1,2}\.\d{1,2}$/.test(value.version)&&/^[a-z][a-z0-9-]{0,23}$/.test(value.variant)&&/^[a-fA-F0-9]{64}$/.test(value.sha256)&&value.url.startsWith('https://github.com/')&&value.tag!==''&&value.license!=='';
  return <Accordion disableGutters sx={{borderRadius:4,overflow:'hidden'}}>
    <AccordionSummary expandIcon={<ExpandMoreRounded/>}><Typography fontWeight={650}>Добавить вариант сборки</Typography></AccordionSummary>
    <AccordionDetails>
      <Stack component="form" spacing={2} onSubmit={async e=>{e.preventDefault();if(!valid||busy)return;setBusy(true);try{await approve(value);}finally{setBusy(false);}}}>
        <Alert severity="info">Укажите доверенную сборку из GitHub Releases. Репозиторий должен входить в XRAY_ALLOWED_REPOSITORIES. Архив проверяется по SHA-256, архитектуре и версии до использования.</Alert>
        <Box sx={{display:'grid',gridTemplateColumns:{xs:'1fr',sm:'1fr 1fr'},gap:2}}>
          <TextField required label="Версия бинарника" value={value.version} onChange={e=>set('version',e.target.value)} placeholder="26.10.17"/>
          <TextField required label="Вариант сборки" value={value.variant} onChange={e=>set('variant',e.target.value)} helperText="Например, compat. Идентификатор: версия-вариант."/>
          <TextField required label="GitHub репозиторий" value={value.repository} onChange={e=>set('repository',e.target.value)}/>
          <TextField required label="Тег релиза" value={value.tag} onChange={e=>set('tag',e.target.value)}/>
          <TextField select label="Архитектура сборки" value={value.arch} onChange={e=>set('arch',e.target.value)}><MenuItem value="amd64">Linux amd64</MenuItem><MenuItem value="arm64">Linux arm64</MenuItem></TextField>
          <TextField required label="Лицензия сборки" value={value.license} onChange={e=>set('license',e.target.value)}/>
        </Box>
        <TextField required label="URL ZIP-архива" type="url" value={value.url} onChange={e=>set('url',e.target.value)}/>
        <TextField required label="SHA-256 архива" value={value.sha256} onChange={e=>set('sha256',e.target.value)} slotProps={{htmlInput:{maxLength:64,spellCheck:false}}}/>
        <Button type="submit" variant="contained" loading={busy} disabled={!valid} sx={{alignSelf:'start'}}>Одобрить и проверить сборку</Button>
      </Stack>
    </AccordionDetails>
  </Accordion>;
}
