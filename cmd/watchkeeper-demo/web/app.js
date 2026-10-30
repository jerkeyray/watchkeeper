const $ = id => document.getElementById(id);
const NS = 'http://www.w3.org/2000/svg';
let scenario = 'lost_confirmation', running = false, currentID = null, polling = null, prev = null;

const scenarios = {
 normal: {kicker: 'Control.', head: 'No fault is injected. Both strategies should commit exactly one effect.', steps: ['Intent saved', 'Email sent', 'Reply received']},
 lost_confirmation: {kicker: 'Reply lost.', head: 'The service commits the email, then the response is dropped before the workflow records it.', steps: ['Intent saved', 'Email sent', 'Reply lost', 'Recovery', 'Decision']},
 no_evidence: {kicker: 'Reply lost, no evidence.', head: 'As above, but the operation declares no verification capability, so recovery cannot query the service.', steps: ['Intent saved', 'Email sent', 'Reply lost', 'Recovery', 'Decision']}
};
const phaseStep = {preparing: 0, prepared: 0, sent: 1, lost: 2, verifying: 3};
const eventNames = {intent_prepared: 'intent prepared', dispatch_authorized: 'dispatch authorized', operation_confirmed: 'receipt confirmed', confirmation_recorded: 'receipt confirmed', recovery_claimed: 'recovery lease claimed', recovery_result: 'evidence → decision'};
const REQ = 'M150 66 H370', RES = 'M370 98 H150', PROBE = 'M330 176 H400 Q430 176 430 146 V122';

async function request(path, options) {
 const res = await fetch(path, options); let data;
 try { data = await res.json(); } catch { throw new Error('The demo server returned an unreadable response.'); }
 if (!res.ok) throw new Error(data.error || `Request failed (${res.status})`);
 return data;
}
function showError(msg) { $('error').textContent = msg; $('error').hidden = !msg; }
function esc(s) { const t = document.createElement('span'); t.textContent = s; return t.innerHTML; }

function controls() {
 $('run').disabled = running; $('run').textContent = running ? 'Running…' : 'Run trial';
 document.querySelectorAll('[data-scenario]').forEach(b => { b.disabled = running; b.classList.toggle('selected', b.dataset.scenario === scenario); });
}
function selectScenario(s) {
 scenario = s; $('kicker').textContent = scenarios[s].kicker; $('headline').innerHTML = scenarios[s].head;
 steps(-1, false); controls();
}
document.querySelectorAll('[data-scenario]').forEach(b => b.addEventListener('click', () => selectScenario(b.dataset.scenario)));

function steps(active, finished, warnAt = -1) {
 $('steps').replaceChildren(...scenarios[scenario].steps.map((n, i) => {
  const li = document.createElement('li'); li.textContent = n;
  if (i < active || (finished && i <= active)) li.className = i === warnAt ? 'warn' : 'done';
  else if (i === active) li.className = 'now';
  return li;
 }));
}

// SVG effects: packets move along a channel using CSS offset-path.
function packet(fx, path, cls, delay = 0) {
 setTimeout(() => {
  const g = document.createElementNS(NS, 'g'); g.setAttribute('class', 'pkt ' + cls);
  g.style.offsetPath = `path('${path}')`;
  const c = document.createElementNS(NS, 'circle'); c.setAttribute('r', cls.includes('reply') ? 5 : 7); g.append(c);
  $(fx).append(g);
  if (!cls.includes('loop')) setTimeout(() => g.remove(), 1300);
 }, delay);
}
function lostReply(fx) {
 packet(fx, RES, 'reply lost');
 setTimeout(() => { const t = document.createElementNS(NS, 'text'); t.setAttribute('class', 'x'); t.setAttribute('x', 260); t.setAttribute('y', 105); t.textContent = '✕'; $(fx).append(t); }, 600);
}
function cls(id, name, on) { $(id).classList.toggle(name, on); }
function pulse(id) { $(id).classList.remove('pulse'); void $(id).getBBox(); $(id).classList.add('pulse'); }

function inbox(id, count, from) {
 if (from === 0 && count > 0) $(id).replaceChildren();
 for (let i = from; i < count; i++) {
  const li = document.createElement('li'); if (i) li.className = 'dup';
  const t = new Date().toLocaleTimeString([], {hour: '2-digit', minute: '2-digit', second: '2-digit'});
  li.innerHTML = `<span class="av">${i + 1}</span><span class="sub">email · order-confirmation</span><time>${t}</time>`;
  $(id).append(li);
 }
}

function reset() {
 prev = {bc: 0, pc: 0, phase: 'preparing', state: null};
 for (const id of ['b-fx', 'w-fx', 'audit']) $(id).replaceChildren();
 for (const id of ['b-inbox', 'w-inbox']) $(id).innerHTML = '<li class="empty">No committed effects</li>';
 $('b-count').textContent = '0'; $('w-count').textContent = '0';
 $('lane-b').className = 'lane'; $('lane-w').className = 'lane keeper';
 for (const id of ['b-src', 'w-src', 'b-dst', 'w-dst', 'w-db', 'w-coord']) $(id).setAttribute('class', $(id).getAttribute('class').split(' ').filter(c => c === 'node' || c === 'small').join(' '));
 $('w-probe').setAttribute('class', 'ch probe'); $('w-probe-lbl').textContent = ''; $('w-probe-lbl').setAttribute('class', 'lbl probe-lbl');
 $('b-verdict').textContent = 'Running.'; $('w-verdict').textContent = 'Running.';
 $('operation-id').textContent = '—'; showError('');
 $('caption').innerHTML = 'Starting a fresh operation…';
 steps(0, false);
}

function render(d) {
 if (!prev) reset();
 const bc = d.baseline.committed_count, pc = d.protected.committed_count, state = d.operation?.state, fault = d.scenario !== 'normal';
 const ob = prev.bc, op = prev.pc, phaseChanged = d.phase !== prev.phase;

 if (state && !prev.state) pulse('w-db');
 // New effects travel along the request channel, then land in the inbox.
 for (let i = ob; i < bc; i++) packet('b-fx', REQ, i ? 'dup' : '', (i - ob) * 250);
 if (bc > ob) setTimeout(() => { inbox('b-inbox', bc, ob); $('b-count').textContent = bc; cls('b-dst', 'lit', true); }, 900 + (bc - ob - 1) * 250);
 if (pc > op) { packet('w-fx', REQ, ''); setTimeout(() => { inbox('w-inbox', pc, op); $('w-count').textContent = pc; cls('w-dst', 'lit', true); }, 900); }

 if (phaseChanged && d.phase === 'complete' && !fault) {
  packet('b-fx', RES, 'reply'); packet('w-fx', RES, 'reply');
  setTimeout(() => { cls('b-src', 'lit', true); cls('w-src', 'lit', true); }, 900);
 }
 if (phaseChanged && d.phase === 'lost') {
  lostReply('b-fx'); lostReply('w-fx');
  setTimeout(() => { cls('b-src', 'down', true); cls('w-src', 'down', true); }, 800);
 }
 if (phaseChanged && d.phase === 'verifying') {
  setTimeout(() => packet('b-fx', RES, 'reply'), 1100);
  $('w-probe').setAttribute('class', 'ch probe on'); cls('w-coord', 'lit', true);
  packet('w-fx', PROBE, 'loop');
  $('w-probe-lbl').textContent = 'GET status?';
 }
 if (state !== prev.state && ['reconciled', 'uncertain'].includes(state)) {
  $('w-fx').querySelectorAll('.loop').forEach(n => n.remove());
  $('w-probe').setAttribute('class', 'ch probe ' + (state === 'reconciled' ? 'found' : 'dead'));
  $('w-probe-lbl').textContent = state === 'reconciled' ? 'committed ✓' : 'no evidence';
  $('w-probe-lbl').setAttribute('class', 'lbl probe-lbl' + (state === 'uncertain' ? ' dead' : ''));
  if (state === 'reconciled') { cls('w-src', 'down', false); cls('w-src', 'lit', true); }
 }

 // Verdicts.
 if (bc > 1) { $('lane-b').className = 'lane bad'; $('b-verdict').textContent = 'Replayed without verification: duplicate effect.'; }
 else if (d.done && bc === 1) { $('lane-b').className = 'lane good'; $('b-verdict').textContent = 'One dispatch, one effect.'; }
 if (state === 'confirmed') { $('lane-w').className = 'lane keeper good'; $('w-verdict').textContent = 'Receipt confirmed: one effect.'; }
 if (state === 'reconciled') { $('lane-w').className = 'lane keeper good'; $('w-verdict').textContent = 'Reconciled from service evidence: no redispatch.'; }
 if (state === 'uncertain') { $('lane-w').className = 'lane keeper unsure'; $('w-verdict').textContent = 'Uncertain: automatic dispatch halted for review.'; }
 if (state === 'prepared' && d.phase === 'verifying') $('w-verdict').textContent = 'Verifying with the service by operation ID.';

 const last = scenarios[d.scenario].steps.length - 1;
 steps(d.phase === 'complete' ? last : phaseStep[d.phase] ?? 0, d.done, fault ? 2 : -1);
 const beat = d.timeline.at(-1);
 if (beat) $('caption').innerHTML = `${esc(beat.title)}<small>${esc(beat.detail)}</small>`;
 if (d.operation) $('operation-id').textContent = d.operation.operation_id;
 $('audit').replaceChildren(...d.events.map(e => { const li = document.createElement('li'); li.textContent = eventNames[e.event_type] || e.event_type.replaceAll('_', ' '); return li; }));
 if (d.error) { showError(d.error); $('caption').innerHTML = 'The demo could not finish.<small>Check that the local services are running.</small>'; }

 prev = {bc, pc, phase: d.phase, state};
 running = !d.done; controls();
}

$('run').addEventListener('click', async () => {
 running = true; controls(); prev = null; reset();
 try {
  const r = await request('/demo/runs', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({scenario})});
  currentID = r.id; await poll();
 } catch (err) { running = false; controls(); showError(err.message); }
});
async function poll() {
 clearTimeout(polling);
 try { const d = await request('/demo/runs/' + encodeURIComponent(currentID)); render(d); if (!d.done) polling = setTimeout(poll, 300); }
 catch (err) { showError(err.message + ' Reconnecting…'); polling = setTimeout(poll, 1500); }
}
async function health() {
 try { const d = await request('/demo/status'); const ok = d.api && d.simulator; $('health').textContent = ok ? 'Live · local services connected' : 'Services offline · run make demo'; $('health-dot').classList.toggle('online', ok); }
 catch { $('health').textContent = 'Demo server disconnected'; $('health-dot').classList.remove('online'); }
 setTimeout(health, 10000);
}
selectScenario(scenario); health();
// ?run=<id> reattaches to an existing run, e.g. after a reload.
const linked = new URLSearchParams(location.search).get('run');
if (linked) request('/demo/runs/' + encodeURIComponent(linked)).then(d => { currentID = linked; selectScenario(d.scenario); running = true; controls(); render(d); if (!d.done) poll(); }).catch(err => showError(err.message));
