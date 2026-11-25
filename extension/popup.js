const $ = (id) => document.getElementById(id);

const serverDot = $('serverDot');
const urlInput = $('urlInput');
const modeSelect = $('modeSelect');
const langSelect = $('langSelect');
const serverUrlInput = $('serverUrlInput');
const settingsPanel = $('settingsPanel');
const toggleSettingsBtn = $('toggleSettingsBtn');
const openWebBtn = $('openWebBtn');
const syncCookiesBtn = $('syncCookiesBtn');
const saveSettingsBtn = $('saveSettingsBtn');
const settingsToast = $('settingsToast');
const playBtn = $('playBtn');
const progressWrap = $('progressWrap');
const progressFill = $('progressFill');
const statusText = $('statusText');
const resultWrap = $('resultWrap');
const resultTitle = $('resultTitle');
const resultText = $('resultText');
const copyBtn = $('copyBtn');
const downloadBtn = $('downloadBtn');

let serverConfig = {};
let videoTitle = 'transcript';

function getServerUrl() {
  return (serverUrlInput.value || 'http://localhost:8080').trim().replace(/\/+$/, '');
}

function showToast(msg) {
  settingsToast.textContent = msg;
  setTimeout(() => {
    if (settingsToast.textContent === msg) settingsToast.textContent = '';
  }, 3000);
}

async function loadInitialState() {
  const stored = await chrome.storage.local.get(['serverUrl', 'mode', 'language']);
  if (stored.serverUrl) serverUrlInput.value = stored.serverUrl;
  if (stored.mode) modeSelect.value = stored.mode;
  if (stored.language) langSelect.value = stored.language;

  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  if (tab?.url && (tab.url.includes('youtube.com/') || tab.url.includes('youtu.be/'))) {
    urlInput.value = tab.url;
  }

  try {
    const res = await fetch(`${getServerUrl()}/api/system`);
    if (!res.ok) throw new Error('offline');
    const data = await res.json();
    serverDot.classList.add('ok');
    serverDot.title = 'ClipText server connected';
    if (data.config) {
      serverConfig = data.config;
      if (!stored.mode && data.config.mode) modeSelect.value = data.config.mode;
      if (!stored.language && data.config.language) langSelect.value = data.config.language;
    }
  } catch {
    serverDot.classList.add('err');
    serverDot.title = 'ClipText server is not running';
  }
}

toggleSettingsBtn.addEventListener('click', () => {
  settingsPanel.classList.toggle('open');
});

openWebBtn.addEventListener('click', () => {
  chrome.tabs.create({ url: getServerUrl() });
});

saveSettingsBtn.addEventListener('click', async () => {
  const base = getServerUrl();
  await chrome.storage.local.set({
    serverUrl: base,
    mode: modeSelect.value,
    language: langSelect.value
  });
  try {
    await fetch(`${base}/api/config`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        ...serverConfig,
        mode: modeSelect.value,
        language: langSelect.value
      })
    });
    showToast('Settings saved');
  } catch {
    showToast('Saved locally (server offline)');
  }
});

syncCookiesBtn.addEventListener('click', async () => {
  syncCookiesBtn.disabled = true;
  try {
    const cookies = await chrome.cookies.getAll({ domain: 'youtube.com' });
    if (!cookies.length) {
      showToast('No youtube.com cookies found');
      return;
    }
    const lines = ['# Netscape HTTP Cookie File'];
    for (const c of cookies) {
      const domain = c.domain.startsWith('.') ? c.domain : '.' + c.domain;
      const includeSub = 'TRUE';
      const path = c.path || '/';
      const secure = c.secure ? 'TRUE' : 'FALSE';
      const exp = c.expirationDate ? Math.floor(c.expirationDate) : 0;
      lines.push([domain, includeSub, path, secure, exp, c.name, c.value].join('\t'));
    }
    const base = getServerUrl();
    const res = await fetch(`${base}/api/cookies`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ content: lines.join('\n') + '\n' })
    });
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || 'Failed to save cookies');
    serverConfig.cookies_file = data.path;
    await fetch(`${base}/api/config`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(serverConfig)
    });
    showToast(`Synced ${cookies.length} YouTube cookies`);
  } catch (err) {
    showToast('Error: ' + err.message);
  } finally {
    syncCookiesBtn.disabled = false;
  }
});

async function readSSE(response, handlers) {
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    const parts = buffer.split('\n\n');
    buffer = parts.pop() || '';
    for (const block of parts) {
      let ev = 'message';
      const dataLines = [];
      for (const line of block.split('\n')) {
        if (line.startsWith('event:')) ev = line.slice(6).trim();
        else if (line.startsWith('data:')) dataLines.push(line.slice(5).trim());
      }
      if (dataLines.length && handlers[ev]) {
        handlers[ev](JSON.parse(dataLines.join('\n')));
      }
    }
  }
}

playBtn.addEventListener('click', async () => {
  const url = urlInput.value.trim();
  if (!url) return;

  playBtn.disabled = true;
  progressWrap.classList.add('visible');
  resultWrap.classList.remove('visible');
  statusText.classList.remove('error');
  progressFill.style.width = '5%';
  statusText.textContent = 'Connecting to server...';

  await chrome.storage.local.set({
    serverUrl: getServerUrl(),
    mode: modeSelect.value,
    language: langSelect.value
  });

  try {
    const res = await fetch(`${getServerUrl()}/api/transcribe`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        ...serverConfig,
        url,
        mode: modeSelect.value,
        language: langSelect.value
      })
    });

    if (!res.ok) {
      const errData = await res.json().catch(() => ({}));
      throw new Error(errData.error || `HTTP ${res.status}`);
    }

    await readSSE(res, {
      meta(m) {
        if (m.title) {
          videoTitle = m.title.replace(/[\\/:*?"<>|]+/g, '_').slice(0, 80);
          resultTitle.textContent = m.title;
        }
      },
      status(s) {
        if (s.message) statusText.textContent = s.message;
      },
      progress(p) {
        const pct = Math.min(100, Math.max(0, Math.round(p.percent || 0)));
        progressFill.style.width = pct + '%';
        if (p.message) statusText.textContent = `${p.message} (${pct}%)`;
      },
      result(r) {
        progressFill.style.width = '100%';
        statusText.textContent = 'Done!';
        resultText.value = r.cleaned_text || r.text || '';
        resultWrap.classList.add('visible');
      },
      error(e) {
        statusText.classList.add('error');
        statusText.textContent = e.message || 'Processing error';
      }
    });
  } catch (err) {
    statusText.classList.add('error');
    statusText.textContent = err.message || 'Could not connect to ClipText server';
  } finally {
    playBtn.disabled = false;
  }
});

copyBtn.addEventListener('click', async () => {
  if (!resultText.value) return;
  await navigator.clipboard.writeText(resultText.value);
  copyBtn.textContent = 'Copied!';
  setTimeout(() => { copyBtn.textContent = 'Copy'; }, 1500);
});

downloadBtn.addEventListener('click', () => {
  if (!resultText.value) return;
  const blob = new Blob([resultText.value], { type: 'text/plain;charset=utf-8' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = `${videoTitle}.txt`;
  a.click();
  URL.revokeObjectURL(url);
});

loadInitialState();
