// Explicitly requested demonstration identity. Changes require a reviewed PR.
// This grants no group membership and applies only to Cognito's admin invite.
export const isApprovedAdminInvite = (event) => {
  const email = event?.request?.userAttributes?.email;
  return event?.triggerSource === 'PreSignUp_AdminCreateUser'
    && typeof email === 'string'
    && email.toLowerCase() === 'demo@atomai.click';
};
