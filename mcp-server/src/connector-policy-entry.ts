// Shared canonical policy for the independently packaged Billing BFF.
// It contains no bearer tokens, signing clients or public transport.
export { dynamoGrantResolver } from './connector-grants.js';
export { effectiveCapabilities } from './connector-contract.js';
