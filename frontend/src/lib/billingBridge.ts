/**
 * Bridge the same-origin Billing iframe uses to obtain Cognito ID tokens from
 * this window (services/billing/page/dash-shim.js reads window.parent.ttobakBilling).
 * Pure and dependency-free so it can be unit tested.
 */

export interface BillingBridge {
  token(): Promise<string>;
  refresh(): Promise<string>;
  signedOut(): void;
}

export interface BillingBridgeDeps {
  getToken: () => string | null;
  refresh: () => Promise<string | null>;
  /** Same teardown api.ts uses on a failed refresh; called at most once per install. */
  onSignedOut: () => void;
}

export type BillingBridgeHost = { ttobakBilling?: BillingBridge };

export function installBillingBridge(win: BillingBridgeHost, deps: BillingBridgeDeps): () => void {
  let signedOutCalled = false;
  // a refresh still in flight after uninstall must not sign out a newer session
  let active = true;
  const signedOut = () => {
    if (!active || signedOutCalled) return;
    signedOutCalled = true;
    deps.onSignedOut();
  };
  const refresh = async (): Promise<string> => {
    let next: string | null = null;
    try {
      next = await deps.refresh();
    } catch {
      next = null;
    }
    if (!next) {
      signedOut();
      throw new Error('session refresh failed');
    }
    return next;
  };
  const token = async (): Promise<string> => deps.getToken() || refresh();
  const bridge: BillingBridge = { token, refresh, signedOut };
  win.ttobakBilling = bridge;
  return () => {
    active = false;
    if (win.ttobakBilling === bridge) delete win.ttobakBilling;
  };
}
