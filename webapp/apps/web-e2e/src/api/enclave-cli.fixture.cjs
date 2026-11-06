// External Enclave CLI peer whose container state outlives attachments.
const fs = require('node:fs');
const path = require('node:path');
const { setTimeout: delay } = require('node:timers/promises');

const root = process.env.HOMESHELL_ENCLAVE_FIXTURE;
const args = process.argv.slice(2);
const fixturePath = (name) => path.join(root, name);
const containerPath = (name) => fixturePath(`container-${name}.json`);

function flag(name, fallback = '') {
  const index = args.indexOf(name);
  return index < 0 ? fallback : args[index + 1];
}

function readContainer(name) {
  try {
    return JSON.parse(fs.readFileSync(containerPath(name), 'utf8'));
  } catch (error) {
    if (error.code === 'ENOENT') return undefined;
    throw error;
  }
}

function saveContainer(container) {
  const destination = containerPath(container.name);
  const temporary = `${destination}.${process.pid}.tmp`;
  fs.writeFileSync(temporary, JSON.stringify(container));
  fs.renameSync(temporary, destination);
}

async function runSession() {
  if (args.includes('--session-monitor'))
    throw new Error('tmux monitor is forbidden');
  // External fault control: a continue that is still building its image.
  if (args[0] === 'continue' && fs.existsSync(fixturePath('block-continue'))) {
    fs.writeFileSync(fixturePath('continue-blocked'), '');
    while (fs.existsSync(fixturePath('block-continue'))) await delay(30);
  }
  if (!args.includes('--background')) throw new Error('must run in background');
  if (fs.existsSync(fixturePath('refuse-start'))) {
    throw new Error('refusing to mount sensitive host paths: fixture refusal');
  }
  const name = `enclave-codex-0123456789ab-${flag('--name')}`;
  saveContainer({
    name,
    session: flag('--name'),
    dir: process.cwd(),
    status: 'running',
    text: 'Ready',
    generation: Date.now(),
  });
  fs.writeFileSync(fixturePath(`gateway-${name}`), '');
  fs.rmSync(fixturePath(`attached-${name}`), { force: true });
  console.log(name);
}

async function listSessions() {
  // External fault control: a Docker query that started but cannot finish.
  if (fs.existsSync(fixturePath('block-inventory'))) {
    fs.writeFileSync(fixturePath('inventory-blocked'), '');
    while (fs.existsSync(fixturePath('block-inventory'))) await delay(30);
  }
  if (fs.existsSync(fixturePath('refuse-inventory')))
    throw new Error('Docker unavailable');
  const containers = [];
  for (const file of fs.readdirSync(root)) {
    if (!file.startsWith('container-') || !file.endsWith('.json')) continue;
    const container = readContainer(
      file.slice('container-'.length, -'.json'.length),
    );
    if (
      container &&
      (container.status === 'running' || args.includes('--all'))
    ) {
      containers.push({
        name: container.name,
        sessionName: container.session,
        tool: 'codex',
        projectDir: container.dir,
        status: container.status,
      });
    }
  }
  console.log(JSON.stringify(containers));
}

async function stopSession(name) {
  if (fs.existsSync(fixturePath(`block-stop-${name}`))) {
    fs.writeFileSync(fixturePath(`stop-blocked-${name}`), '');
    while (fs.existsSync(fixturePath(`block-stop-${name}`))) await delay(30);
  }
  fs.rmSync(containerPath(name), { force: true });
  fs.rmSync(fixturePath(`gateway-${name}`), { force: true });
}

async function attachSession(name) {
  if (!readContainer(name)) throw new Error('session missing');
  // Expose the bridge PID as an external peer can; no daemon records are read.
  fs.appendFileSync(fixturePath('attaches'), `${process.ppid}\n`);
  fs.writeFileSync(fixturePath(`attach-pid-${name}`), String(process.ppid));
  // External fault control: an agent that dies at startup, before drawing its UI.
  if (fs.existsSync(fixturePath('crash-on-start'))) {
    process.stdout.write('fatal: fixture crash\r\n');
    await delay(500);
    saveContainer({ ...readContainer(name), status: 'exited' });
    process.exit(1);
  }
  const attached = fixturePath(`attached-${name}`);
  if (!fs.existsSync(attached)) process.stdout.write('\x1b]0;✳ Codex\x07');
  fs.writeFileSync(attached, '');

  let lastText;
  let lastTitle;
  let pending = '';
  process.stdin.setRawMode(true);
  process.stdin.setEncoding('utf8');
  process.stdin.on('data', (chunk) => {
    const container = readContainer(name);
    if (!container || container.status !== 'running') return;
    if (chunk.includes('\x1b[A')) {
      container.text = 'key:up';
      saveContainer(container);
      chunk = chunk.replaceAll('\x1b[A', '');
    }
    pending += chunk.replaceAll('\r', '\n');
    while (pending.includes('\n')) {
      const end = pending.indexOf('\n');
      const command = pending.slice(0, end);
      pending = pending.slice(end + 1);
      if (command === 'exit') container.status = 'exited';
      else container.text = `received: ${command}`;
      if (command === 'working') container.title = '⠋ Codex';
      if (command === 'ready') container.title = '✳ Codex';
      saveContainer(container);
    }
  });
  process.stdin.on('end', () => process.exit(0));
  while (true) {
    const container = readContainer(name);
    if (!container || container.status !== 'running') process.exit(0);
    if (container.title && container.title !== lastTitle) {
      process.stdout.write(`\x1b]0;${container.title}\x07`);
      lastTitle = container.title;
    }
    if (container.text !== lastText) {
      process.stdout.write(`\x1b[2J\x1b[H${container.text}\r\n`);
      lastText = container.text;
    }
    await delay(30);
  }
}

async function main() {
  fs.appendFileSync(fixturePath('calls'), `${JSON.stringify(args)}\n`);
  switch (args[0]) {
    case 'tools':
      console.log(
        JSON.stringify({
          schemaVersion: '1',
          kind: 'tool',
          extensions: [{ name: 'codex', displayName: 'Codex', enabled: true }],
        }),
      );
      break;
    case 'config': {
      // `--pass-env +NAME` adds to the (here empty) configured list, as Enclave
      // does; `old-pass-env` keeps the "+" as an Enclave before #181 did.
      const literal = fs.existsSync(fixturePath('old-pass-env'));
      const passEnv = args.flatMap((arg, i) =>
        arg === '--pass-env'
          ? [literal ? args[i + 1] : args[i + 1].replace(/^\+/, '')]
          : [],
      );
      console.log(
        JSON.stringify({
          schemaVersion: '1',
          kind: 'config',
          options: passEnv.length
            ? { pass_env: { kind: 'string_slice', value: passEnv } }
            : {},
          valid: true,
        }),
      );
      break;
    }
    case 'features':
      console.log(
        JSON.stringify({ schemaVersion: '1', kind: 'feature', extensions: [] }),
      );
      break;
    case 'run':
    case 'continue':
      await runSession();
      break;
    case 'ps':
      await listSessions();
      break;
    case 'stop':
      await stopSession(args[1]);
      break;
    case 'attach':
      await attachSession(args[1]);
      break;
    default:
      throw new Error(`unsupported operation: ${args[0]}`);
  }
}

main().catch((error) => {
  console.error(error.message);
  process.exit(1);
});
