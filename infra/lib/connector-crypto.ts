import * as lambda from 'aws-cdk-lib/aws-lambda';
import { Construct } from 'constructs';
import { execFileSync } from 'node:child_process';
import { join } from 'node:path';

/** Cross-platform wheel selection prevents a workstation's x86 extension from
 * silently entering the ARM64 Python Lambda. Hashes pin the complete dependency set. */
export function connectorCryptoLayer(scope: Construct): lambda.LayerVersion {
  const input = join(__dirname, '../assets/connector-crypto');
  const install = ['-m', 'pip', 'install', '--disable-pip-version-check', '--no-compile',
    '--require-hashes', '--no-deps', '--only-binary=:all:', '--platform', 'manylinux2014_aarch64',
    '--python-version', '3.12', '--implementation', 'cp', '--abi', 'cp312'];
  return new lambda.LayerVersion(scope, 'ConnectorCryptoLayer', {
    description: 'Pinned ARM64 Python 3.12 RSA verification dependencies',
    compatibleArchitectures: [lambda.Architecture.ARM_64],
    compatibleRuntimes: [lambda.Runtime.PYTHON_3_12],
    code: lambda.Code.fromAsset(input, {
      bundling: {
        image: lambda.Runtime.PYTHON_3_12.bundlingImage,
        platform: 'linux/arm64',
        command: ['python3', ...install, '-r', '/asset-input/requirements.txt', '-t', '/asset-output/python'],
        local: {
          tryBundle(outputDir: string) {
            const python = ['python3.12', 'python3'].find(command => {
              try {
                execFileSync(command, ['-c', 'import sys, pip; assert sys.version_info[:2] == (3, 12)'],
                  { stdio: 'ignore' });
                return true;
              } catch { return false; }
            });
            if (!python) return false;
            // A failed install is fatal, rather than accepting a partial artifact.
            execFileSync(python, [...install, '-r', join(input, 'requirements.txt'),
              '-t', join(outputDir, 'python')], { stdio: 'inherit', timeout: 180_000 });
            return true;
          },
        },
      },
    }),
  });
}
