import { execFileSync } from 'node:child_process';
import { join } from 'node:path';

test('prepared release coordinator passes its isolated state-machine checks', () => {
  execFileSync('python3', ['-m', 'unittest', 'discover', '-s', 'scripts/operations', '-p', 'test_http_origin_release.py'], {
    cwd: join(__dirname, '../..'), timeout: 30_000, encoding: 'utf8', stdio: 'pipe',
  });
});
