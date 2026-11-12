// Minimal zip writer for tests: stored or deflate entries with a Unix mode.
// `declared` overrides the uncompressed size in both headers (to fake a lie).
import { crc32, deflateRawSync } from 'node:zlib';

export function makeZip(entries) {
  const locals = [];
  const centrals = [];
  let offset = 0;
  for (const entry of entries) {
    const name = Buffer.from(entry.name, 'utf8');
    const raw = Buffer.from(entry.data ?? '');
    const method = entry.method === 'deflate' ? 8 : 0;
    const body = method === 8 ? deflateRawSync(raw) : raw;
    const size = entry.declared ?? raw.length;
    const mode = entry.mode ?? (entry.name.endsWith('/') ? 0o040755 : 0o100644);
    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt16LE(entry.flags ?? 0, 6);
    local.writeUInt16LE(method, 8);
    local.writeUInt32LE(crc32(raw), 14);
    local.writeUInt32LE(body.length, 18);
    local.writeUInt32LE(size, 22);
    local.writeUInt16LE(name.length, 26);
    locals.push(local, name, body);
    const central = Buffer.alloc(46);
    central.writeUInt32LE(0x02014b50, 0);
    central.writeUInt16LE((3 << 8) | 20, 4);
    central.writeUInt16LE(20, 6);
    central.writeUInt16LE(entry.flags ?? 0, 8);
    central.writeUInt16LE(method, 10);
    central.writeUInt32LE(crc32(raw), 16);
    central.writeUInt32LE(body.length, 20);
    central.writeUInt32LE(size, 24);
    central.writeUInt16LE(name.length, 28);
    central.writeUInt32LE((mode << 16) >>> 0, 38);
    central.writeUInt32LE(offset, 42);
    centrals.push(central, name);
    offset += local.length + name.length + body.length;
  }
  const directory = Buffer.concat(centrals);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(entries.length, 8);
  end.writeUInt16LE(entries.length, 10);
  end.writeUInt32LE(directory.length, 12);
  end.writeUInt32LE(offset, 16);
  return Buffer.concat([...locals, directory, end]);
}

export const DEMO_SCRIPT = `#!/usr/bin/env bash
VERSION="9.9.9"
TOOLBOX_REGISTRIES=(
  "s3://example-registry-one/tools.json"
  # a comment line
  "s3://example-registry-two/tools.json"
)
TOOLBOX_MCPS_BASE=(alpha-mcp beta-mcp)
TOOLBOX_MCPS_EXTRA=(extra-mcp)
AIM_MCPS_BASE=(gamma-mcp delta-mcp)
AIM_MCPS_EXTRA=(
  never-mcp other-mcp
)
HTTP_MCPS=("docs-mcp=https://docs.example.com/mcp")
TOOLBOX_MCPS=("\${TOOLBOX_MCPS_BASE[@]}")
echo "$HOME"
`;

export function demoPackage(...extra) {
  return [
    { name: 'pkg-v9.9.9/README.md', data: 'readme' },
    { name: 'pkg-v9.9.9/setup-internal-mcp.sh', data: DEMO_SCRIPT },
    { name: 'pkg-v9.9.9/skills/demo-skill/SKILL.md', data: '# demo' },
    { name: 'pkg-v9.9.9/skills/demo-skill/scripts/run.py', data: 'print(1)', method: 'deflate' },
    ...extra,
  ];
}
