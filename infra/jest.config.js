module.exports = {
  testEnvironment: 'node',
  // Shared self-hosted nodes expose many CPUs even when the runner's memory
  // request is small. Bound concurrent CDK/TypeScript workers; run every suite.
  maxWorkers: 2,
  roots: ['<rootDir>/test'],
  testMatch: ['**/*.test.ts'],
  transform: {
    '^.+\\.tsx?$': 'ts-jest'
  },
  setupFilesAfterEnv: ['aws-cdk-lib/testhelpers/jest-autoclean'],
};
