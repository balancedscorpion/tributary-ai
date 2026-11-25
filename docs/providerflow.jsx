import { useState, useEffect } from "react";

/* ═══════════════════════ ICONS ═══════════════════════ */
const I = {
  Plus:()=><svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round"><line x1="7" y1="2" x2="7" y2="12"/><line x1="2" y1="7" x2="12" y2="7"/></svg>,
  Check:()=><svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round"><polyline points="2.5 7 5.5 10 11.5 4"/></svg>,
  Arr:({d="r"})=><svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" style={d==="l"?{transform:"rotate(180deg)"}:{}}><line x1="2" y1="7" x2="12" y2="7"/><polyline points="8 3 12 7 8 11"/></svg>,
  Edit:()=><svg width="13" height="13" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M10 2l2 2L5 11H3V9z"/></svg>,
  Trash:()=><svg width="13" height="13" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M2 4h10M5 4V3a1 1 0 011-1h2a1 1 0 011 1v1M4 4v8a1 1 0 001 1h4a1 1 0 001-1V4"/></svg>,
  X:()=><svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round"><line x1="3" y1="3" x2="11" y2="11"/><line x1="11" y1="3" x2="3" y2="11"/></svg>,
  Key:()=><svg width="13" height="13" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><circle cx="9" cy="5" r="3"/><path d="M6.5 7.5L2 12M4 10l1.5 1.5"/></svg>,
  Zap:()=><svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><polygon points="8 1 3 8 7 8 6 13 11 6 7 6 8 1"/></svg>,
  Globe:()=><svg width="16" height="16" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round"><circle cx="7" cy="7" r="5.5"/><ellipse cx="7" cy="7" rx="2.5" ry="5.5"/><line x1="1.5" y1="7" x2="12.5" y2="7"/></svg>,
  Shield:()=><svg width="13" height="13" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M7 1L2 3.5v3.5c0 3 2.2 5.5 5 6 2.8-.5 5-3 5-6V3.5L7 1z"/></svg>,
  Cloud:()=><svg width="15" height="15" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round"><path d="M3.5 10.5A3 3 0 013 4.6a4 4 0 017.8-.1A2.5 2.5 0 0111.5 9.5H3.5z"/></svg>,
  Eye:()=><svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round"><path d="M1 7s2.5-4 6-4 6 4 6 4-2.5 4-6 4-6-4-6-4z"/><circle cx="7" cy="7" r="1.5"/></svg>,
  EyeOff:()=><svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round"><path d="M2 2l10 10M5.6 5.6a1.5 1.5 0 002.8 2.8M1 7s2.5-4 6-4c.8 0 1.5.2 2.2.5M13 7s-1.2 2-3.2 3.2"/></svg>,
  Cog:()=><svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round"><circle cx="7" cy="7" r="2"/><path d="M7 1v1M7 12v1M1 7h1M12 7h1M2.8 2.8l.7.7M10.5 10.5l.7.7M11.2 2.8l-.7.7M3.5 10.5l-.7.7"/></svg>,
  Refresh:()=><svg width="13" height="13" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M1 2v4h4M13 12V8H9"/><path d="M2 8a5 5 0 018.5-3L13 8M12 6a5 5 0 01-8.5 3L1 6"/></svg>,
};

const redactKey = (k) => {
  if (!k || k.length < 8) return "••••••••••";
  return k.slice(0, Math.min(7, k.length - 3)) + "••••••" + k.slice(-3);
};

/* Secret field: set → shows redacted + clear/change. Unset → shows input + set button */
function SecretField({ label, placeholder, value, isSet, onSet, onClear, optional }) {
  const [draft, setDraft] = useState("");
  const [editing, setEditing] = useState(false);
  if (isSet && !editing) {
    return (
      <div style={s.fRow}>
        <label style={s.fLabel}>{label}{optional && <span style={s.optTag}>optional</span>}</label>
        <div style={s.secretSet}>
          <div style={s.secretRedacted}>
            <I.Key /><span style={s.secretMask}>{redactKey(value)}</span>
            <span style={s.setBadge}>Set</span>
          </div>
          <div style={{display:"flex",gap:4}}>
            <button style={s.secBtn} onClick={() => { setEditing(true); setDraft(""); }}>Change</button>
            <button style={{...s.secBtn,color:"#EF4444"}} onClick={onClear}>Clear</button>
          </div>
        </div>
      </div>
    );
  }
  return (
    <div style={s.fRow}>
      <label style={s.fLabel}>{label}{optional && <span style={s.optTag}>optional</span>}</label>
      <div style={{display:"flex",gap:6,alignItems:"center"}}>
        <input type="password" placeholder={placeholder} value={draft} onChange={e => setDraft(e.target.value)} style={{...s.input,flex:1}} />
        <button style={{...s.setKeyBtn,...(draft.trim()?{}:s.dis)}} disabled={!draft.trim()} onClick={() => { onSet(draft); setDraft(""); setEditing(false); }}>Set</button>
        {editing && <button style={s.secCancelBtn} onClick={() => setEditing(false)}>Cancel</button>}
      </div>
    </div>
  );
}

/* ═══════════════════════ PROVIDER DEFS ═══════════════════════ */
const PROVIDERS = {
  anthropic: {
    id:"anthropic", name:"Anthropic", desc:"Direct API access", icon:"A", color:"#D97757", cloudCred:null,
    authMethods: [{ id:"api_key", label:"API Key", desc:"Anthropic API key", fields:[{key:"apiKey",label:"API Key",placeholder:"sk-ant-...",secret:true}] }],
    config: [
      {key:"model",label:"Model",placeholder:"claude-opus-4-6",def:"claude-opus-4-6",type:"text",group:"config"},
      {key:"baseUrl",label:"Base URL (optional)",placeholder:"https://api.anthropic.com/",def:"https://api.anthropic.com/",type:"text",group:"config"},
    ]
  },
  bedrock: {
    id:"bedrock", name:"AWS Bedrock", desc:"Anthropic via AWS", icon:"▲", color:"#FF9900", cloudCred:"aws",
    authMethods: [
      {id:"aws_credentials",label:"AWS Cloud Credentials",desc:"Use saved AWS credentials from Cloud Credentials tab",fields:[],usesCloudCred:true},
      {id:"bedrock_api_key",label:"Bedrock API Key",desc:"Single API key for Bedrock access",fields:[{key:"apiKey",label:"Bedrock API Key",placeholder:"Enter Bedrock API key",secret:true}]}
    ],
    config: [
      {key:"model",label:"Model",placeholder:"anthropic.claude-opus-4-6-v1",def:"anthropic.claude-opus-4-6-v1",type:"text",group:"config"},
      {key:"region",label:"Region",placeholder:"us-east-1",def:"us-east-1",type:"text",group:"config"},
      {key:"baseUrl",label:"Base URL (optional)",placeholder:"https://bedrock-runtime.us-east-1.amazonaws.com",def:"",type:"text",group:"config"},
      {key:"anthropicVersion",label:"Anthropic Version",placeholder:"bedrock-2023-05-31",def:"bedrock-2023-05-31",type:"text",group:"config"},
      {key:"reasoningEffort",label:"Reasoning Effort",def:"",type:"select",options:["Default","low","medium","high"],group:"advanced"},
      {key:"timeout",label:"Timeout (ms)",placeholder:"No timeout",def:"",type:"number",group:"advanced"},
    ]
  },
  openai: {
    id:"openai", name:"OpenAI", desc:"OpenAI Responses API", icon:"◎", color:"#10A37F", cloudCred:null,
    authMethods: [{id:"api_key",label:"API Key",desc:"Your OpenAI API key",fields:[{key:"apiKey",label:"API Key",placeholder:"sk-...",secret:true}]}],
    config: [
      {key:"model",label:"Model",placeholder:"gpt-4o",def:"gpt-4o",type:"text",group:"config"},
      {key:"baseUrl",label:"Base URL (optional)",placeholder:"https://api.openai.com/v1",def:"",type:"text",group:"config"},
    ]
  },
  gemini: {
    id:"gemini", name:"Gemini", desc:"Google Gemini API", icon:"✦", color:"#4285F4", cloudCred:"gcp",
    authMethods: [
      {id:"gcp_adc",label:"GCP Application Default Credentials",desc:"Use saved GCP credentials from Cloud Credentials tab",fields:[],usesCloudCred:true},
      {id:"api_key",label:"API Key",desc:"Direct Gemini API key",fields:[{key:"apiKey",label:"Gemini API Key",placeholder:"AI...",secret:true}]}
    ],
    config: [{key:"model",label:"Model",placeholder:"gemini-2.0-flash",def:"gemini-2.0-flash",type:"text",group:"config"}]
  },
  compatible: {
    id:"compatible", name:"OpenAI Compatible", desc:"Any compatible endpoint", icon:"⟡", color:"#8B8B8B", cloudCred:null,
    authMethods: [
      {id:"api_key",label:"API Key",desc:"Standard API key auth",fields:[{key:"apiKey",label:"API Key",placeholder:"Enter API key",secret:true}]},
      {id:"none",label:"No Authentication",desc:"For VPC or pre-authed endpoints",fields:[]}
    ],
    config: [
      {key:"displayName",label:"Display Name",placeholder:"Custom Provider",def:"Custom Provider",type:"text",group:"identity"},
      {key:"apiType",label:"API Type",def:"chat_completions",type:"select",options:["chat_completions","responses"],group:"config"},
      {key:"model",label:"Model",placeholder:"Enter model name",def:"",type:"text",group:"config"},
      {key:"baseUrl",label:"Base URL",placeholder:"https://your-endpoint.com/v1",def:"",type:"text",group:"config"},
      {key:"authHeader",label:"Auth Header",placeholder:"Authorization",def:"Authorization",type:"text",group:"auth_detail"},
      {key:"authScheme",label:"Auth Scheme",placeholder:"Bearer",def:"Bearer",type:"text",group:"auth_detail"},
      {key:"modelInBody",label:"Include model in request body",def:true,type:"toggle",group:"placement"},
      {key:"modelInPath",label:"Include model in URL path",def:false,type:"toggle",group:"placement"},
      {key:"endpointSuffix",label:"Endpoint Suffix",placeholder:"/beta",def:"",type:"text",group:"advanced"},
      {key:"timeout",label:"Timeout (ms)",placeholder:"No timeout",def:"",type:"number",group:"advanced"},
      {key:"reasoningEffort",label:"Reasoning Effort",def:"",type:"select",options:["Default","low","medium","high"],group:"advanced"},
    ]
  },
};

/* ═══════════════════════ MAIN ═══════════════════════ */
export default function App() {
  const [tab, setTab] = useState("providers");

  // Cloud credentials
  const [cCreds, setCCreds] = useState([{id:"aws-1",type:"aws",name:"Production AWS",accessKeyId:"AKIAIOSFODNN7EXAMPLE",isSet:true,region:"us-east-1"}]);
  const [cWiz, setCWiz] = useState(false);
  const [cType, setCType] = useState(null);
  const [cFields, setCFields] = useState({});
  const [cSec, setCSec] = useState({});
  const [cDel, setCDel] = useState(null);

  // Provider state
  const [saved, setSaved] = useState([]);
  const [wiz, setWiz] = useState(false);
  const [step, setStep] = useState(0);
  const [selProv, setSelProv] = useState(null);
  const [selAuth, setSelAuth] = useState(null);
  const [selCC, setSelCC] = useState(null);
  const [fld, setFld] = useState({});
  const [sec, setSec] = useState({});
  const [secV, setSecV] = useState({});
  const [tst, setTst] = useState("idle");
  const [editId, setEditId] = useState(null);
  const [pDel, setPDel] = useState(null);
  const [showAdv, setShowAdv] = useState(false);
  const [xHdrs, setXHdrs] = useState([]);
  const [inlineCC, setInlineCC] = useState(false);

  const prov = selProv ? PROVIDERS[selProv] : null;
  const auth = selAuth !== null && prov ? prov.authMethods[selAuth] : null;
  const multi = prov?.authMethods.length > 1;
  const matchCC = prov?.cloudCred ? cCreds.filter(c => c.type === prov.cloudCred) : [];

  const resetP = () => { setWiz(false);setStep(0);setSelProv(null);setSelAuth(null);setSelCC(null);setFld({});setSec({});setSecV({});setTst("idle");setEditId(null);setShowAdv(false);setXHdrs([]);setInlineCC(false); };
  const resetC = () => { setCWiz(false);setCType(null);setCFields({});setCSec({}); };

  useEffect(() => {
    if (prov) { const d = {}; prov.config.forEach(f => { if (f.def !== undefined && f.def !== "") d[f.key] = f.def; }); setFld(prev => ({ ...d, ...prev })); }
  }, [selProv]);

  const canProceed = () => {
    if (!auth) return false;
    for (const f of auth.fields) { if (f.secret && !sec[f.key]) return false; if (!f.secret && !f.optional && !fld[f.key]?.trim()) return false; }
    if (auth.usesCloudCred && !selCC) return false;
    return true;
  };

  const handleTest = () => { setTst("testing"); setTimeout(() => setTst("success"), 1800); };
  const handleSaveP = () => {
    const e = { id: editId || Date.now().toString(), providerId:selProv, providerName:prov.name, icon:prov.icon, color:prov.color, authLabel:auth.label, model:fld.model||"", region:fld.region||"", cloudCredName:selCC?cCreds.find(c=>c.id===selCC)?.name:null, displayName:fld.displayName||prov.name, apiType:fld.apiType||"" };
    if (editId) setSaved(prev => prev.map(p => p.id === editId ? e : p)); else setSaved(prev => [...prev, e]);
    resetP();
  };
  const handleSaveC = () => {
    const e = { id:Date.now().toString(), type:cType, name:cFields.name||(cType==="aws"?"AWS Credentials":"GCP Credentials"), isSet:true, accessKeyId:cFields.accessKeyId||"", region:cFields.region||"us-east-1", projectId:cFields.projectId||"" };
    setCCreds(prev => [...prev, e]); resetC();
  };

  const stepLabels = multi ? ["Provider","Auth","Configure","Test"] : ["Provider","Configure","Test"];
  const stepMap = multi ? [0,1,2,3] : [0,2,3];

  const configGroups = prov ? [
    { key:"identity",label:"Identity",icon:<I.Edit />,fields:prov.config.filter(f=>f.group==="identity") },
    { key:"config",label:"Configuration",icon:<I.Zap />,fields:prov.config.filter(f=>f.group==="config") },
    { key:"auth_detail",label:"Auth Details",icon:<I.Key />,fields:prov.config.filter(f=>f.group==="auth_detail"), hide: auth?.id==="none" },
    { key:"placement",label:"Model Placement",icon:<I.Cog />,fields:prov.config.filter(f=>f.group==="placement") },
  ].filter(g => g.fields.length > 0 && !g.hide) : [];

  const advFields = prov ? prov.config.filter(f => f.group === "advanced") : [];

  return (
    <div style={s.root}>
      <style>{`
        @import url('https://fonts.googleapis.com/css2?family=DM+Sans:ital,opsz,wght@0,9..40,300;0,9..40,400;0,9..40,500;0,9..40,600&family=DM+Mono:wght@400;500&display=swap');
        *{box-sizing:border-box;margin:0;padding:0}
        input:focus,select:focus{border-color:#444 !important;outline:none}
        input::placeholder{color:#444}
        button:hover:not(:disabled){filter:brightness(1.12)}
        @keyframes fadeIn{from{opacity:0;transform:translateY(5px)}to{opacity:1;transform:translateY(0)}}
        @keyframes slideUp{from{opacity:0;transform:translateY(12px)}to{opacity:1;transform:translateY(0)}}
        @keyframes scaleIn{from{opacity:0;transform:scale(0.95)}to{opacity:1;transform:scale(1)}}
        @keyframes spin{to{transform:rotate(360deg)}}
        @keyframes pulse{0%,100%{opacity:1}50%{opacity:0.4}}
        @keyframes checkDraw{from{stroke-dashoffset:20}to{stroke-dashoffset:0}}
        .scrollable::-webkit-scrollbar{width:4px} .scrollable::-webkit-scrollbar-thumb{background:#333;border-radius:2px}
      `}</style>

      <div style={s.panel}>
        <div style={s.header}>
          <div style={s.hdrL}><span style={s.gear}>⚙</span><span style={s.hdrT}>Settings</span></div>
          <button style={s.closeBtn}><I.X /></button>
        </div>

        <div style={s.tabs}>
          {[["providers","Providers"],["cloud","Cloud Credentials"],["system","System Prompt"],["advanced","Advanced"]].map(([k,l])=>(
            <button key={k} style={{...s.tab,...(tab===k?s.tabOn:{})}} onClick={()=>setTab(k)}>{l}</button>
          ))}
        </div>

        <div style={s.content} className="scrollable">

        {/* ══════════ PROVIDERS LIST ══════════ */}
        {tab==="providers" && !wiz && (
          <div style={{animation:"fadeIn .25s ease"}}>
            {saved.length===0?(
              <div style={s.empty}><div style={s.emptyIco}><I.Globe /></div><p style={s.emptyT}>No providers configured yet</p><p style={s.emptySub}>Add a provider to connect to an LLM API</p><button style={s.addPri} onClick={()=>{resetP();setWiz(true);}}><I.Plus /> New Provider</button></div>
            ):(
              <>
                <div style={s.list}>
                  {saved.map((e,i)=>(
                    <div key={e.id} style={{...s.card,animationDelay:`${i*50}ms`}}>
                      <div style={s.cL}>
                        <div style={{...s.cIco,background:e.color+"14",color:e.color,borderColor:e.color+"30"}}>{e.icon}</div>
                        <div>
                          <div style={s.cNm}>{e.displayName||e.providerName}</div>
                          <div style={s.cMeta}>
                            {e.model&&<span style={s.mn}>{e.model}</span>}
                            {e.region&&<><span style={s.dt}>·</span><span style={s.mn}>{e.region}</span></>}
                            {e.apiType&&<><span style={s.dt}>·</span><span style={s.mn}>{e.apiType}</span></>}
                            <span style={s.dt}>·</span><span>{e.authLabel}</span>
                            {e.cloudCredName&&<><span style={s.dt}>·</span><span style={{display:"inline-flex",alignItems:"center",gap:3}}><I.Cloud />{e.cloudCredName}</span></>}
                          </div>
                        </div>
                      </div>
                      <div style={s.cR}>
                        <div style={s.stRow}><div style={s.stDot}/><span style={s.stTxt}>Connected</span></div>
                        <div style={s.acts}>
                          <button style={s.actBtn} onClick={()=>{setEditId(e.id);setSelProv(e.providerId);setStep(0);setWiz(true);}}><I.Edit /></button>
                          {pDel===e.id?(<div style={{display:"flex",gap:4}}><button style={s.dY} onClick={()=>{setSaved(p=>p.filter(x=>x.id!==e.id));setPDel(null);}}>Delete</button><button style={s.dN} onClick={()=>setPDel(null)}>Cancel</button></div>):(<button style={s.actBtn} onClick={()=>setPDel(e.id)}><I.Trash /></button>)}
                        </div>
                      </div>
                    </div>
                  ))}
                </div>
                <button style={s.addSec} onClick={()=>{resetP();setWiz(true);}}><I.Plus /> Add Provider</button>
              </>
            )}
          </div>
        )}

        {/* ══════════ PROVIDER WIZARD ══════════ */}
        {tab==="providers" && wiz && (
          <div style={{animation:"scaleIn .2s ease"}}>
            <div style={s.prog}>{stepLabels.map((l,i)=>{const my=stepMap[i],ac=step===my,dn=step>my;return(<div key={i} style={s.pI}><div style={{...s.pD,borderColor:ac?"#D97757":dn?"#34D399":"#333",color:ac?"#D97757":dn?"#34D399":"#555",background:ac?"#D9775712":dn?"#34D39912":"transparent"}}>{dn?<I.Check />:<span style={{fontSize:11}}>{i+1}</span>}</div><span style={{...s.pL,color:ac?"#E8E0D8":dn?"#7B7770":"#444"}}>{l}</span>{i<stepLabels.length-1&&<div style={{...s.pLn,background:dn?"#34D39940":"#1E1E1C"}}/>}</div>);})}</div>

            {/* Step 0 */}
            {step===0&&(<div style={{animation:"slideUp .25s ease"}}><h3 style={s.sT}>Choose a provider</h3><p style={s.sD}>Select which LLM service to connect to</p>
              <div style={s.grid}>{Object.values(PROVIDERS).map(p=>(<button key={p.id} style={{...s.pOpt,...(selProv===p.id?{borderColor:p.color,background:p.color+"08",boxShadow:`0 0 0 1px ${p.color}40`}:{})}} onClick={()=>{setSelProv(p.id);setSelAuth(PROVIDERS[p.id].authMethods.length===1?0:null);}}><div style={{...s.pOI,color:p.color,background:p.color+"14",borderColor:p.color+"30"}}>{p.icon}</div><div style={s.pON}>{p.name}</div><div style={s.pOD}>{p.desc}</div>{selProv===p.id&&<div style={{...s.selCk,background:p.color}}><I.Check /></div>}</button>))}</div>
              <div style={s.ft}><button style={s.gh} onClick={resetP}>Cancel</button><button style={{...s.pr,...(!selProv?s.dis:{})}} disabled={!selProv} onClick={()=>setStep(multi?1:2)}>Continue <I.Arr /></button></div>
            </div>)}

            {/* Step 1 */}
            {step===1&&multi&&(<div style={{animation:"slideUp .25s ease"}}><h3 style={s.sT}>Choose authentication</h3><p style={s.sD}>How to authenticate with {prov.name}?</p>
              <div style={s.aOpts}>{prov.authMethods.map((a,i)=>{const nc=a.usesCloudCred,hc=nc&&matchCC.length>0;return(<button key={a.id} style={{...s.aOpt,...(selAuth===i?{borderColor:prov.color,background:prov.color+"08",boxShadow:`0 0 0 1px ${prov.color}40`}:{})}} onClick={()=>setSelAuth(i)}><div style={s.aHd}><div style={{...s.rad,borderColor:selAuth===i?prov.color:"#444"}}>{selAuth===i&&<div style={{...s.radIn,background:prov.color}}/>}</div><div style={{flex:1}}><div style={{display:"flex",alignItems:"center",gap:8,flexWrap:"wrap"}}><span style={s.aLbl}>{a.label}</span>{nc&&hc&&<span style={s.ccOk}><I.Check /> Available</span>}{nc&&!hc&&<span style={s.ccNo}>Not configured</span>}</div><div style={s.aDsc}>{a.desc}</div></div></div>{a.fields.length>0&&<div style={s.pills}>{a.fields.map(f=><span key={f.key} style={s.pill}>{f.label}</span>)}</div>}</button>);})}</div>
              <div style={s.ft}><button style={s.gh} onClick={()=>setStep(0)}><I.Arr d="l" /> Back</button><button style={{...s.pr,...(selAuth===null?s.dis:{})}} disabled={selAuth===null} onClick={()=>setStep(2)}>Continue <I.Arr /></button></div>
            </div>)}

            {/* Step 2: Configure */}
            {step===2&&auth&&(<div style={{animation:"slideUp .25s ease"}}><h3 style={s.sT}>Configure {prov.name}</h3><p style={s.sD}><span style={{...s.badge,borderColor:prov.color+"40",color:prov.color,background:prov.color+"10"}}>{auth.usesCloudCred?<I.Cloud />:<I.Key />} {auth.label}</span></p>

              {/* Cloud cred picker */}
              {auth.usesCloudCred&&(<div style={s.fGrp}><div style={s.fGL}><I.Cloud /> Cloud Credentials</div>
                {matchCC.length>0?(<>
                  <div style={{display:"flex",flexDirection:"column",gap:6,marginBottom:8}}>
                    {matchCC.map(cc=>(<button key={cc.id} style={{...s.ccOpt,...(selCC===cc.id?{borderColor:prov.color,background:prov.color+"08"}:{})}} onClick={()=>setSelCC(cc.id)}>
                      <div style={{...s.rad,borderColor:selCC===cc.id?prov.color:"#444",width:16,height:16}}>{selCC===cc.id&&<div style={{...s.radIn,background:prov.color,width:6,height:6}}/>}</div>
                      <div><div style={{fontSize:13,fontWeight:500,color:"#E8E0D8"}}>{cc.name}</div><div style={{fontSize:11,color:"#666"}}>{cc.type==="aws"?`${redactKey(cc.accessKeyId)} · ${cc.region}`:`Project: ${cc.projectId||"—"}`}</div></div>
                      <span style={{...s.setBadge,marginLeft:"auto"}}>Configured</span>
                    </button>))}
                  </div>
                  <button style={s.ccAdd} onClick={()=>setInlineCC(true)}><I.Plus /> Add new {prov.cloudCred==="aws"?"AWS":"GCP"} credentials</button>
                </>):(
                  <div style={s.ccEmpty}><p style={{fontSize:13,color:"#888",marginBottom:10}}>No {prov.cloudCred==="aws"?"AWS":"GCP"} credentials configured.</p>
                    <button style={{...s.addPri,fontSize:13,padding:"8px 16px"}} onClick={()=>{setTab("cloud");setCWiz(true);setCType(prov.cloudCred);}}><I.Plus /> Configure in Cloud Credentials</button>
                    <p style={{fontSize:11,color:"#555",marginTop:8}}>You'll be taken to the Cloud Credentials tab.</p>
                  </div>
                )}
                {inlineCC&&(<div style={s.inCC}>
                  <div style={{display:"flex",justifyContent:"space-between",alignItems:"center",marginBottom:10}}><span style={{fontSize:12,fontWeight:600,color:"#D97757"}}>Quick Add: {prov.cloudCred==="aws"?"AWS":"GCP"}</span><button style={s.closeBtn} onClick={()=>setInlineCC(false)}><I.X /></button></div>
                  <div style={s.fRow}><label style={s.fLabel}>Name</label><input style={s.input} placeholder="e.g. Production AWS" value={cFields.name||""} onChange={e=>setCFields(p=>({...p,name:e.target.value}))}/></div>
                  {prov.cloudCred==="aws"&&<>
                    <SecretField label="Access Key ID" placeholder="AKIA..." isSet={cSec.ak} value={cFields.ak} onSet={v=>{setCFields(p=>({...p,ak:v}));setCSec(p=>({...p,ak:true}));}} onClear={()=>{setCSec(p=>({...p,ak:false}));}}/>
                    <SecretField label="Secret Access Key" placeholder="Enter secret" isSet={cSec.sk} value={cFields.sk} onSet={v=>{setCFields(p=>({...p,sk:v}));setCSec(p=>({...p,sk:true}));}} onClear={()=>{setCSec(p=>({...p,sk:false}));}}/>
                    <SecretField label="Session Token" placeholder="Optional" optional isSet={cSec.st} value={cFields.st} onSet={v=>{setCFields(p=>({...p,st:v}));setCSec(p=>({...p,st:true}));}} onClear={()=>{setCSec(p=>({...p,st:false}));}}/>
                    <div style={s.fRow}><label style={s.fLabel}>Region</label><input style={{...s.input,...s.mInput}} placeholder="us-east-1" value={cFields.region||""} onChange={e=>setCFields(p=>({...p,region:e.target.value}))}/></div>
                  </>}
                  <button style={{...s.pr,width:"100%",justifyContent:"center",marginTop:6}} onClick={()=>{const nc={id:Date.now().toString(),type:prov.cloudCred,name:cFields.name||(prov.cloudCred==="aws"?"AWS Credentials":"GCP Credentials"),isSet:true,accessKeyId:cFields.ak||"",region:cFields.region||"us-east-1"};setCCreds(p=>[...p,nc]);setSelCC(nc.id);setInlineCC(false);setCFields({});setCSec({});}}><I.Check /> Save Credential</button>
                </div>)}
              </div>)}

              {/* Auth fields */}
              {auth.fields.length>0&&(<div style={s.fGrp}><div style={s.fGL}><I.Shield /> Authentication</div>
                {auth.fields.map(f=>f.secret?(
                  <SecretField key={f.key} label={f.label} placeholder={f.placeholder} isSet={sec[f.key]} value={secV[f.key]}
                    onSet={v=>{setSec(p=>({...p,[f.key]:true}));setSecV(p=>({...p,[f.key]:v}));}} onClear={()=>{setSec(p=>({...p,[f.key]:false}));setSecV(p=>({...p,[f.key]:""}));}} optional={f.optional}/>
                ):(<div key={f.key} style={s.fRow}><label style={s.fLabel}>{f.label}</label><input placeholder={f.placeholder} value={fld[f.key]||""} onChange={e=>setFld(p=>({...p,[f.key]:e.target.value}))} style={s.input}/></div>))}
              </div>)}

              {/* Config groups */}
              {configGroups.map(g=>(<div key={g.key} style={s.fGrp}>
                <div style={s.fGL}>{g.icon} {g.label}</div>
                {g.fields.map(f=>(<div key={f.key} style={s.fRow}>
                  {f.type==="toggle"?(
                    <label style={s.tgl}><input type="checkbox" checked={fld[f.key]??f.def} onChange={e=>setFld(p=>({...p,[f.key]:e.target.checked}))} style={{display:"none"}}/>
                      <div style={{...s.tTr,...((fld[f.key]??f.def)?s.tOn:{})}}><div style={{...s.tTh,...((fld[f.key]??f.def)?s.tThOn:{})}}/></div>
                      <span style={{fontSize:13,color:"#CCC"}}>{f.label}</span></label>
                  ):(
                    <><label style={s.fLabel}>{f.label}</label>
                    {f.type==="select"?(<select style={s.sel} value={fld[f.key]??f.def??""} onChange={e=>setFld(p=>({...p,[f.key]:e.target.value}))}>{f.options.map(o=><option key={o} value={o}>{o}</option>)}</select>
                    ):(<input type={f.type==="number"?"number":"text"} placeholder={f.placeholder} value={fld[f.key]??""} onChange={e=>setFld(p=>({...p,[f.key]:e.target.value}))} style={{...s.input,...(f.type!=="number"?s.mInput:{})}}/>)}</>
                  )}
                </div>))}
              </div>))}

              {/* Advanced */}
              {advFields.length>0&&(<div style={s.advTgl}><button style={s.advBtn} onClick={()=>setShowAdv(!showAdv)}><span style={{transform:showAdv?"rotate(90deg)":"rotate(0deg)",transition:"transform .15s",display:"inline-block"}}>▸</span> Advanced Options</button>
                {showAdv&&(<div style={{...s.fGrp,marginTop:8,animation:"fadeIn .2s ease"}}>{advFields.map(f=>(<div key={f.key} style={s.fRow}><label style={s.fLabel}>{f.label}</label>{f.type==="select"?(<select style={s.sel} value={fld[f.key]??f.def??""} onChange={e=>setFld(p=>({...p,[f.key]:e.target.value}))}>{f.options.map(o=><option key={o} value={o}>{o}</option>)}</select>):(<input type={f.type==="number"?"number":"text"} placeholder={f.placeholder} value={fld[f.key]??""} onChange={e=>setFld(p=>({...p,[f.key]:e.target.value}))} style={s.input}/>)}</div>))}</div>)}
              </div>)}

              {/* Extra Headers */}
              <div style={s.fGrp}>
                <div style={{...s.fGL,justifyContent:"space-between"}}><span style={{display:"flex",alignItems:"center",gap:6}}><I.Globe /> Extra Headers</span><button style={s.hdrAdd} onClick={()=>setXHdrs(p=>[...p,{id:Date.now(),key:"",value:"",isSecret:false}])}><I.Plus /> Add</button></div>
                {xHdrs.length===0?<p style={{fontSize:12,color:"#555",textAlign:"center",padding:"6px 0"}}>No extra headers configured.</p>:xHdrs.map((h,i)=>(<div key={h.id} style={s.hdrRow}><input style={{...s.input,...s.mInput,flex:1}} placeholder="Header name" value={h.key} onChange={e=>{const n=[...xHdrs];n[i]={...n[i],key:e.target.value};setXHdrs(n);}}/><input style={{...s.input,...s.mInput,flex:1}} type={h.isSecret?"password":"text"} placeholder="Value" value={h.value} onChange={e=>{const n=[...xHdrs];n[i]={...n[i],value:e.target.value};setXHdrs(n);}}/><button style={s.hdrIBtn} onClick={()=>{const n=[...xHdrs];n[i]={...n[i],isSecret:!n[i].isSecret};setXHdrs(n);}}>{h.isSecret?<I.EyeOff />:<I.Eye />}</button><button style={{...s.hdrIBtn,color:"#EF4444"}} onClick={()=>setXHdrs(p=>p.filter(x=>x.id!==h.id))}><I.Trash /></button></div>))}
              </div>

              <div style={s.ft}><button style={s.gh} onClick={()=>setStep(multi?1:0)}><I.Arr d="l" /> Back</button><button style={{...s.pr,...(!canProceed()?s.dis:{})}} disabled={!canProceed()} onClick={()=>{setStep(3);setTst("idle");}}>Continue <I.Arr /></button></div>
            </div>)}

            {/* Step 3: Test */}
            {step===3&&(<div style={{animation:"slideUp .25s ease"}}><h3 style={s.sT}>Test connection</h3><p style={s.sD}>Verify configuration before saving</p>
              <div style={s.sum}>{[["Provider",<span><span style={{color:prov.color,marginRight:6}}>{prov.icon}</span>{prov.name}</span>],["Auth",auth.label],...(auth.usesCloudCred&&selCC?[["Cloud Cred",cCreds.find(c=>c.id===selCC)?.name||"—"]]:[]),...(fld.displayName&&fld.displayName!==prov.name?[["Name",fld.displayName]]:[]),["Model",<span style={s.mn}>{fld.model||"—"}</span>],...(fld.region?[["Region",<span style={s.mn}>{fld.region}</span>]]:[]),...(fld.apiType?[["API Type",<span style={s.mn}>{fld.apiType}</span>]]:[])]
                .map(([l,v],i,a)=>(<div key={String(l)}><div style={s.smR}><span style={s.smL}>{l}</span><span style={s.smV}>{v}</span></div>{i<a.length-1&&<div style={s.smD}/>}</div>))}</div>

              <div style={s.tArea}>
                {tst==="idle"&&<button style={{...s.tBtn,borderColor:prov.color,color:prov.color}} onClick={handleTest}><I.Zap /> Test Connection</button>}
                {tst==="testing"&&<div style={s.tIng}><div style={{...s.spin,borderTopColor:prov.color}}/><span style={s.tIngTxt}>Testing connection...</span></div>}
                {tst==="success"&&<div style={{...s.tRes,borderColor:"#34D39940",background:"#34D39908"}}><div style={s.okI}><svg width="22" height="22" viewBox="0 0 22 22" fill="none" stroke="#34D399" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round"><polyline points="5 11 9 15 17 7" style={{strokeDasharray:20,animation:"checkDraw .4s ease forwards"}}/></svg></div><div><div style={{fontSize:14,fontWeight:500,color:"#34D399",marginBottom:2}}>Connection successful</div><div style={{fontSize:12,color:"#777"}}>API responded — model: {fld.model||"—"}</div></div></div>}
              </div>

              <div style={s.ft}><button style={s.gh} onClick={()=>setStep(2)}><I.Arr d="l" /> Back</button><div style={{display:"flex",gap:8}}>{tst==="success"&&<button style={s.gh} onClick={()=>setTst("idle")}><I.Refresh /> Re-test</button>}<button style={{...s.sv,...(tst!=="success"?s.dis:{})}} disabled={tst!=="success"} onClick={handleSaveP}><I.Check /> Save Provider</button></div></div>
            </div>)}
          </div>
        )}

        {/* ══════════ CLOUD CREDENTIALS TAB ══════════ */}
        {tab==="cloud"&&!cWiz&&(
          <div style={{animation:"fadeIn .25s ease"}}>
            <div style={{marginBottom:16}}><h3 style={s.sT}>Cloud Credentials</h3><p style={{...s.sD,marginBottom:0}}>Manage cloud platform credentials for use with providers</p></div>
            {cCreds.length===0?(
              <div style={s.empty}><div style={s.emptyIco}><I.Cloud /></div><p style={s.emptyT}>No cloud credentials configured</p><p style={s.emptySub}>Add AWS or GCP credentials for use with providers</p><button style={s.addPri} onClick={()=>{resetC();setCWiz(true);}}><I.Plus /> Add Cloud Credential</button></div>
            ):(
              <>
                <div style={s.list}>{cCreds.map((cc,i)=>(<div key={cc.id} style={{...s.card,animationDelay:`${i*50}ms`}}>
                  <div style={s.cL}>
                    <div style={{...s.cIco,background:cc.type==="aws"?"#FF990014":"#4285F414",color:cc.type==="aws"?"#FF9900":"#4285F4",borderColor:cc.type==="aws"?"#FF990030":"#4285F430"}}>{cc.type==="aws"?"▲":"G"}</div>
                    <div><div style={s.cNm}>{cc.name}</div><div style={s.cMeta}><span style={s.mn}>{cc.type==="aws"?redactKey(cc.accessKeyId):`Project: ${cc.projectId||"—"}`}</span>{cc.region&&<><span style={s.dt}>·</span><span style={s.mn}>{cc.region}</span></>}</div></div>
                  </div>
                  <div style={s.cR}>
                    <span style={s.setBadge}>Stored securely</span>
                    {cDel===cc.id?(<div style={{display:"flex",gap:4}}><button style={s.dY} onClick={()=>{setCCreds(p=>p.filter(x=>x.id!==cc.id));setCDel(null);}}>Delete</button><button style={s.dN} onClick={()=>setCDel(null)}>Cancel</button></div>):(<button style={s.actBtn} onClick={()=>setCDel(cc.id)}><I.Trash /></button>)}
                  </div>
                </div>))}</div>
                <button style={s.addSec} onClick={()=>{resetC();setCWiz(true);}}><I.Plus /> Add Cloud Credential</button>
              </>
            )}
          </div>
        )}

        {/* Cloud Cred Wizard */}
        {tab==="cloud"&&cWiz&&(
          <div style={{animation:"scaleIn .2s ease"}}>
            {!cType?(<div style={{animation:"slideUp .25s ease"}}><h3 style={s.sT}>Choose cloud platform</h3><p style={s.sD}>Select the cloud platform to configure</p>
              <div style={{display:"grid",gridTemplateColumns:"1fr 1fr",gap:10,marginBottom:20}}>{[{id:"aws",name:"Amazon Web Services",icon:"▲",color:"#FF9900",desc:"Access Key + Secret"},{id:"gcp",name:"Google Cloud Platform",icon:"G",color:"#4285F4",desc:"Project ID & ADC"}].map(c=>(<button key={c.id} style={s.pOpt} onClick={()=>{setCType(c.id);setCFields({});}}><div style={{...s.pOI,color:c.color,background:c.color+"14",borderColor:c.color+"30"}}>{c.icon}</div><div style={s.pON}>{c.name}</div><div style={s.pOD}>{c.desc}</div></button>))}</div>
              <div style={s.ft}><button style={s.gh} onClick={resetC}>Cancel</button></div>
            </div>):(<div style={{animation:"slideUp .25s ease"}}><h3 style={s.sT}>Configure {cType==="aws"?"AWS":"GCP"}</h3><p style={s.sD}>These credentials will be available to any provider that needs them</p>
              <div style={s.fGrp}><div style={s.fGL}><I.Shield /> {cType==="aws"?"AWS":"GCP"} Authentication</div>
                <div style={s.fRow}><label style={s.fLabel}>Display Name</label><input style={s.input} placeholder={cType==="aws"?"e.g. Production AWS":"e.g. My GCP Project"} value={cFields.name||""} onChange={e=>setCFields(p=>({...p,name:e.target.value}))}/></div>
                {cType==="aws"?(<>
                  <SecretField label="Access Key ID" placeholder="AKIA..." isSet={cSec.accessKeyId} value={cFields.accessKeyId} onSet={v=>{setCFields(p=>({...p,accessKeyId:v}));setCSec(p=>({...p,accessKeyId:true}));}} onClear={()=>{setCSec(p=>({...p,accessKeyId:false}));setCFields(p=>({...p,accessKeyId:""}));}}/>
                  <SecretField label="Secret Access Key" placeholder="Enter secret key" isSet={cSec.secretAccessKey} value={cFields.secretAccessKey} onSet={v=>{setCFields(p=>({...p,secretAccessKey:v}));setCSec(p=>({...p,secretAccessKey:true}));}} onClear={()=>{setCSec(p=>({...p,secretAccessKey:false}));}}/>
                  <SecretField label="Session Token" placeholder="Optional" optional isSet={cSec.sessionToken} value={cFields.sessionToken} onSet={v=>{setCFields(p=>({...p,sessionToken:v}));setCSec(p=>({...p,sessionToken:true}));}} onClear={()=>{setCSec(p=>({...p,sessionToken:false}));}}/>
                  <div style={s.fRow}><label style={s.fLabel}>Default Region</label><input style={{...s.input,...s.mInput}} placeholder="us-east-1" value={cFields.region||""} onChange={e=>setCFields(p=>({...p,region:e.target.value}))}/></div>
                </>):(<>
                  <div style={s.fRow}><label style={s.fLabel}>Project ID</label><input style={{...s.input,...s.mInput}} placeholder="my-gcp-project-id" value={cFields.projectId||""} onChange={e=>setCFields(p=>({...p,projectId:e.target.value}))}/></div>
                  <div style={{marginTop:8,padding:12,background:"#0E0E0D",border:"1px solid #1E1E1C",borderRadius:8}}><div style={{display:"flex",justifyContent:"space-between",marginBottom:8}}><span style={{fontSize:13,fontWeight:500,color:"#CCC"}}>Application Default Credentials</span><button style={s.gh}><I.Refresh /> Refresh</button></div><div style={{display:"flex",alignItems:"center",gap:8,padding:"8px 10px",background:"#34D39908",border:"1px solid #34D39930",borderRadius:6}}><I.Check /><div><div style={{fontSize:12,fontWeight:500,color:"#34D399"}}>ADC detected (authorized_user)</div><div style={{fontSize:11,color:"#666",fontFamily:"'DM Mono',monospace"}}>~/.config/gcloud/application_default_credentials.json</div></div></div></div>
                </>)}
              </div>
              <div style={s.ft}><button style={s.gh} onClick={()=>setCType(null)}><I.Arr d="l" /> Back</button><button style={s.sv} onClick={handleSaveC}><I.Check /> Save Credential</button></div>
            </div>)}
          </div>
        )}

        {/* Placeholder tabs */}
        {tab==="system"&&<div style={{...s.empty,animation:"fadeIn .25s ease"}}><div style={s.emptyIco}><I.Cog /></div><p style={s.emptyT}>System Prompt</p><p style={s.emptySub}>Configure default system prompt</p></div>}
        {tab==="advanced"&&<div style={{...s.empty,animation:"fadeIn .25s ease"}}><div style={s.emptyIco}><I.Cog /></div><p style={s.emptyT}>Advanced</p><p style={s.emptySub}>Advanced configuration options</p></div>}

        </div>
      </div>

      {/* Sidebar */}
      <div style={s.anno}>
        <div style={s.anT}>UX Flow — v2</div>
        {[["Cloud Credentials tab","AWS & GCP configured independently in their own tab. Reusable across providers."],
          ["Provider ↔ Cloud linking","Wizard detects available cloud creds. Pick existing or create inline."],
          ["Exclusive auth choice","Radio between cloud creds vs API key. Never shown simultaneously."],
          ["Full config exposed","All spec options: API type, model placement, auth header/scheme, timeouts, reasoning effort, extra headers."],
          ["API key redaction","Set keys show redacted (sk-ant-••••3f). Not retrievable — only Set / Clear."],
          ["Test before save","Connection test required before save. Success/fail feedback with re-test."],
          ["Extra headers","Key/value with secret toggle. Secret values stored encrypted."],
        ].map(([t,d],i)=>(<div key={i} style={s.anI}><span style={s.anB}>{i+1}</span><div><div style={s.anIT}>{t}</div><div style={s.anID}>{d}</div></div></div>))}
        <div style={s.anH}>Try: Add Bedrock (pick AWS cred) → then Compatible (full config with toggles, auth details, advanced).</div>
      </div>
    </div>
  );
}

/* ═══════════════════════ STYLES ═══════════════════════ */
const s={
  root:{minHeight:"100vh",background:"#0A0A0A",display:"flex",alignItems:"flex-start",justifyContent:"center",padding:"24px 14px",gap:"18px",fontFamily:"'DM Sans',sans-serif",color:"#E8E0D8",flexWrap:"wrap"},
  panel:{width:"100%",maxWidth:660,background:"#111110",borderRadius:14,border:"1px solid #1E1E1C",overflow:"hidden",boxShadow:"0 8px 40px rgba(0,0,0,0.5),inset 0 0 0 1px rgba(255,255,255,0.02)"},
  header:{display:"flex",alignItems:"center",justifyContent:"space-between",padding:"13px 18px",borderBottom:"1px solid #1A1A18"},
  hdrL:{display:"flex",alignItems:"center",gap:10},gear:{fontSize:17,color:"#D97757"},hdrT:{fontSize:15,fontWeight:600,letterSpacing:"-0.01em"},
  closeBtn:{background:"none",border:"none",color:"#555",cursor:"pointer",padding:5,borderRadius:6,display:"flex",alignItems:"center"},
  tabs:{display:"flex",gap:0,padding:"0 6px",borderBottom:"1px solid #1A1A18",background:"#0D0D0C",overflowX:"auto"},
  tab:{background:"none",border:"none",color:"#555",padding:"10px 12px",fontSize:12,fontWeight:500,cursor:"pointer",borderBottom:"2px solid transparent",fontFamily:"'DM Sans',sans-serif",whiteSpace:"nowrap"},
  tabOn:{color:"#E8E0D8",borderBottomColor:"#D97757"},
  content:{padding:"18px 16px",minHeight:400,maxHeight:"72vh",overflowY:"auto"},
  empty:{display:"flex",flexDirection:"column",alignItems:"center",justifyContent:"center",padding:"48px 20px",textAlign:"center"},
  emptyIco:{width:42,height:42,borderRadius:10,background:"#1A1A18",border:"1px solid #252523",display:"flex",alignItems:"center",justifyContent:"center",color:"#555",marginBottom:12},
  emptyT:{fontSize:14,fontWeight:500,color:"#888",marginBottom:4},emptySub:{fontSize:13,color:"#555",marginBottom:18},
  addPri:{display:"flex",alignItems:"center",gap:8,padding:"9px 17px",background:"#D97757",color:"#fff",border:"none",borderRadius:8,fontSize:13,fontWeight:500,cursor:"pointer",fontFamily:"'DM Sans',sans-serif",boxShadow:"0 2px 8px #D9775740"},
  list:{display:"flex",flexDirection:"column",gap:7,marginBottom:12},
  card:{display:"flex",alignItems:"center",justifyContent:"space-between",padding:"11px 13px",background:"#161614",border:"1px solid #222220",borderRadius:10,animation:"fadeIn .3s ease both",flexWrap:"wrap",gap:8},
  cL:{display:"flex",alignItems:"center",gap:10,minWidth:0,flex:1},cIco:{width:32,height:32,borderRadius:7,border:"1px solid",display:"flex",alignItems:"center",justifyContent:"center",fontSize:14,fontWeight:600,flexShrink:0},
  cNm:{fontSize:13,fontWeight:500,marginBottom:1},cMeta:{display:"flex",alignItems:"center",gap:5,fontSize:11,color:"#777",flexWrap:"wrap"},
  mn:{fontFamily:"'DM Mono',monospace",color:"#999",fontSize:11},mInput:{fontFamily:"'DM Mono',monospace",fontSize:13},dt:{color:"#3A3A38"},
  cR:{display:"flex",alignItems:"center",gap:10,flexShrink:0},stRow:{display:"flex",alignItems:"center",gap:5},stDot:{width:6,height:6,borderRadius:"50%",background:"#34D399",boxShadow:"0 0 6px #34D39966"},stTxt:{fontSize:11,color:"#34D399",fontWeight:500},
  acts:{display:"flex",alignItems:"center",gap:3},actBtn:{background:"none",border:"none",color:"#555",cursor:"pointer",padding:5,borderRadius:5,display:"flex",alignItems:"center"},
  dY:{background:"#EF4444",border:"none",color:"#fff",padding:"3px 10px",borderRadius:4,fontSize:11,fontWeight:500,cursor:"pointer",fontFamily:"'DM Sans',sans-serif"},
  dN:{background:"#222",border:"none",color:"#999",padding:"3px 10px",borderRadius:4,fontSize:11,fontWeight:500,cursor:"pointer",fontFamily:"'DM Sans',sans-serif"},
  addSec:{display:"flex",alignItems:"center",gap:6,padding:"7px 14px",background:"none",color:"#777",border:"1px dashed #2A2A28",borderRadius:8,fontSize:12,fontWeight:500,cursor:"pointer",fontFamily:"'DM Sans',sans-serif",width:"100%",justifyContent:"center"},
  prog:{display:"flex",alignItems:"center",justifyContent:"center",marginBottom:22,paddingTop:8,paddingBottom:4},
  pI:{display:"flex",alignItems:"center",gap:6},pD:{width:22,height:22,borderRadius:"50%",border:"2px solid #333",display:"flex",alignItems:"center",justifyContent:"center",fontSize:11,fontWeight:600,flexShrink:0,transition:"all .2s"},
  pL:{fontSize:11,fontWeight:500,whiteSpace:"nowrap",transition:"color .2s"},pLn:{width:22,height:2,borderRadius:1,margin:"0 4px",transition:"background .2s"},
  sT:{fontSize:15,fontWeight:600,marginBottom:4,letterSpacing:"-0.01em"},sD:{fontSize:13,color:"#777",marginBottom:16},
  grid:{display:"grid",gridTemplateColumns:"repeat(auto-fill,minmax(150px,1fr))",gap:7,marginBottom:18},
  pOpt:{position:"relative",display:"flex",flexDirection:"column",alignItems:"flex-start",padding:"12px 11px",background:"#161614",border:"1px solid #222220",borderRadius:9,cursor:"pointer",textAlign:"left",fontFamily:"'DM Sans',sans-serif",transition:"all .15s"},
  pOI:{width:26,height:26,borderRadius:6,border:"1px solid",display:"flex",alignItems:"center",justifyContent:"center",fontSize:12,fontWeight:700,marginBottom:7},
  pON:{fontSize:12,fontWeight:500,marginBottom:1,color:"#E8E0D8"},pOD:{fontSize:10,color:"#666",lineHeight:1.4},
  selCk:{position:"absolute",top:6,right:6,width:17,height:17,borderRadius:"50%",display:"flex",alignItems:"center",justifyContent:"center",color:"#fff",animation:"scaleIn .2s ease"},
  aOpts:{display:"flex",flexDirection:"column",gap:7,marginBottom:18},
  aOpt:{display:"flex",flexDirection:"column",gap:8,padding:13,background:"#161614",border:"1px solid #222220",borderRadius:9,cursor:"pointer",textAlign:"left",fontFamily:"'DM Sans',sans-serif",transition:"all .15s"},
  aHd:{display:"flex",alignItems:"flex-start",gap:10},
  rad:{width:18,height:18,borderRadius:"50%",border:"2px solid #444",display:"flex",alignItems:"center",justifyContent:"center",flexShrink:0,marginTop:1,transition:"border-color .15s"},
  radIn:{width:8,height:8,borderRadius:"50%",animation:"scaleIn .15s ease"},
  aLbl:{fontSize:13,fontWeight:500,color:"#E8E0D8"},aDsc:{fontSize:12,color:"#777",marginTop:2},
  ccOk:{display:"inline-flex",alignItems:"center",gap:4,fontSize:10,color:"#34D399",fontWeight:500,background:"#34D39910",padding:"2px 7px",borderRadius:4},
  ccNo:{fontSize:10,color:"#FF9900",fontWeight:500,background:"#FF990010",padding:"2px 7px",borderRadius:4},
  pills:{display:"flex",gap:5,flexWrap:"wrap",marginLeft:28},pill:{padding:"2px 7px",background:"#1A1A18",border:"1px solid #252523",borderRadius:4,fontSize:10,color:"#777",fontFamily:"'DM Mono',monospace"},
  fGrp:{marginBottom:12,padding:12,background:"#141412",border:"1px solid #1E1E1C",borderRadius:9},
  fGL:{display:"flex",alignItems:"center",gap:6,fontSize:11,fontWeight:600,color:"#777",textTransform:"uppercase",letterSpacing:"0.05em",marginBottom:10},
  fRow:{marginBottom:8},fLabel:{display:"flex",alignItems:"center",gap:8,fontSize:12,fontWeight:500,color:"#999",marginBottom:4},
  optTag:{fontSize:10,color:"#555",fontStyle:"italic",fontWeight:400},
  input:{width:"100%",padding:"7px 10px",background:"#0E0E0D",border:"1px solid #252523",borderRadius:6,color:"#E8E0D8",fontSize:13,fontFamily:"'DM Sans',sans-serif"},
  sel:{width:"100%",padding:"7px 10px",background:"#0E0E0D",border:"1px solid #252523",borderRadius:6,color:"#E8E0D8",fontSize:13,fontFamily:"'DM Sans',sans-serif"},
  badge:{display:"inline-flex",alignItems:"center",gap:6,padding:"3px 9px",borderRadius:6,border:"1px solid",fontSize:12,fontWeight:500},
  tgl:{display:"flex",alignItems:"center",gap:10,cursor:"pointer"},
  tTr:{width:34,height:18,borderRadius:9,background:"#2A2A28",position:"relative",transition:"background .2s",flexShrink:0},tOn:{background:"#D97757"},
  tTh:{width:14,height:14,borderRadius:7,background:"#666",position:"absolute",top:2,left:2,transition:"all .2s"},tThOn:{background:"#fff",left:18},
  advTgl:{marginBottom:12},advBtn:{background:"none",border:"none",color:"#888",fontSize:12,fontWeight:500,cursor:"pointer",fontFamily:"'DM Sans',sans-serif",display:"flex",alignItems:"center",gap:6},
  hdrAdd:{display:"flex",alignItems:"center",gap:4,background:"none",border:"1px solid #2A2A28",borderRadius:5,color:"#888",fontSize:10,fontWeight:500,cursor:"pointer",fontFamily:"'DM Sans',sans-serif",padding:"2px 7px"},
  hdrRow:{display:"flex",gap:5,alignItems:"center",marginBottom:5},hdrIBtn:{background:"none",border:"none",color:"#666",cursor:"pointer",padding:3,borderRadius:4,display:"flex",alignItems:"center",flexShrink:0},
  // Secret field
  secretSet:{display:"flex",alignItems:"center",justifyContent:"space-between",padding:"6px 9px",background:"#0E0E0D",border:"1px solid #252523",borderRadius:6,gap:6,flexWrap:"wrap"},
  secretRedacted:{display:"flex",alignItems:"center",gap:7,color:"#999",fontSize:12},secretMask:{fontFamily:"'DM Mono',monospace",fontSize:11,color:"#777",letterSpacing:"0.5px"},
  setBadge:{fontSize:9,fontWeight:600,color:"#34D399",background:"#34D39912",padding:"2px 6px",borderRadius:3,letterSpacing:"0.03em"},
  secBtn:{background:"none",border:"1px solid #2A2A28",borderRadius:5,color:"#999",fontSize:11,fontWeight:500,cursor:"pointer",fontFamily:"'DM Sans',sans-serif",padding:"2px 9px"},
  setKeyBtn:{background:"#D97757",border:"none",borderRadius:6,color:"#fff",fontSize:12,fontWeight:500,cursor:"pointer",fontFamily:"'DM Sans',sans-serif",padding:"6px 14px",whiteSpace:"nowrap"},
  secCancelBtn:{background:"none",border:"1px solid #2A2A28",borderRadius:6,color:"#999",fontSize:12,fontWeight:500,cursor:"pointer",fontFamily:"'DM Sans',sans-serif",padding:"6px 10px"},
  ccOpt:{display:"flex",alignItems:"center",gap:10,padding:"9px 11px",background:"#161614",border:"1px solid #222220",borderRadius:7,cursor:"pointer",textAlign:"left",fontFamily:"'DM Sans',sans-serif",transition:"all .15s",width:"100%"},
  ccAdd:{display:"flex",alignItems:"center",gap:6,background:"none",border:"1px dashed #333",borderRadius:6,color:"#777",fontSize:11,cursor:"pointer",fontFamily:"'DM Sans',sans-serif",padding:"6px 10px",width:"100%",justifyContent:"center"},
  ccEmpty:{padding:14,background:"#1A1A18",borderRadius:8,textAlign:"center"},
  inCC:{marginTop:8,padding:12,background:"#1A1A18",border:"1px solid #2A2A28",borderRadius:9},
  sum:{padding:12,background:"#141412",border:"1px solid #1E1E1C",borderRadius:9,marginBottom:14},
  smR:{display:"flex",alignItems:"center",justifyContent:"space-between",padding:"5px 0"},smL:{fontSize:12,color:"#777"},smV:{fontSize:13,fontWeight:500,color:"#E8E0D8"},smD:{height:1,background:"#1E1E1C"},
  tArea:{display:"flex",justifyContent:"center",padding:"14px 0",minHeight:60},
  tBtn:{display:"flex",alignItems:"center",gap:8,padding:"9px 18px",background:"transparent",border:"1.5px solid",borderRadius:8,fontSize:13,fontWeight:500,cursor:"pointer",fontFamily:"'DM Sans',sans-serif"},
  tIng:{display:"flex",alignItems:"center",gap:12},spin:{width:18,height:18,border:"2px solid #333",borderRadius:"50%",animation:"spin .8s linear infinite"},tIngTxt:{fontSize:13,color:"#888",animation:"pulse 1.5s ease infinite"},
  tRes:{display:"flex",alignItems:"center",gap:12,padding:"11px 13px",border:"1px solid",borderRadius:9,width:"100%",animation:"scaleIn .25s ease"},
  okI:{width:28,height:28,display:"flex",alignItems:"center",justifyContent:"center",flexShrink:0},
  ft:{display:"flex",alignItems:"center",justifyContent:"space-between",paddingTop:14,borderTop:"1px solid #1A1A18",marginTop:4},
  gh:{display:"flex",alignItems:"center",gap:5,padding:"6px 12px",background:"none",border:"1px solid #2A2A28",borderRadius:6,color:"#999",fontSize:12,fontWeight:500,cursor:"pointer",fontFamily:"'DM Sans',sans-serif"},
  pr:{display:"flex",alignItems:"center",gap:5,padding:"6px 14px",background:"#D97757",border:"none",borderRadius:6,color:"#fff",fontSize:12,fontWeight:500,cursor:"pointer",fontFamily:"'DM Sans',sans-serif",boxShadow:"0 2px 8px #D9775730"},
  sv:{display:"flex",alignItems:"center",gap:5,padding:"6px 14px",background:"#166534",border:"none",borderRadius:6,color:"#34D399",fontSize:12,fontWeight:600,cursor:"pointer",fontFamily:"'DM Sans',sans-serif"},
  dis:{opacity:0.35,cursor:"not-allowed",pointerEvents:"none"},
  anno:{width:"100%",maxWidth:250,padding:14,background:"#111110",border:"1px solid #1E1E1C",borderRadius:13,fontSize:11,color:"#888",lineHeight:1.5,position:"sticky",top:24},
  anT:{fontSize:12,fontWeight:600,color:"#D97757",marginBottom:12},
  anI:{display:"flex",gap:8,alignItems:"flex-start",marginBottom:10},anB:{display:"flex",alignItems:"center",justifyContent:"center",minWidth:17,height:17,borderRadius:4,background:"#D9775716",color:"#D97757",fontSize:9,fontWeight:700,flexShrink:0,marginTop:1},
  anIT:{fontSize:11,fontWeight:600,color:"#C8C0B8",marginBottom:1},anID:{fontSize:10,color:"#666",lineHeight:1.4},
  anH:{padding:"8px 9px",background:"#1A1A18",borderRadius:6,fontSize:10,color:"#999",lineHeight:1.5,borderLeft:"2px solid #D9775740",marginTop:6},
};
