import { PASSWORD_REQUIREMENTS } from './password-policy';

// Cognito names are stable control signals; never infer account state by parsing
// a provider's English message or expose a new unauthenticated lookup endpoint.
export function authErrorMessage(error: unknown, fallback: string): string {
  const name = error instanceof Error ? error.name : '';
  const messages: Record<string, string> = {
    PasswordResetRequiredException: '비밀번호 재설정이 필요합니다. 비밀번호 찾기에서 받은 코드를 입력하거나 새 코드를 요청해주세요.',
    UserNotFoundException: '이메일을 확인해주세요. 아직 초대받지 않았다면 관리자에게 사용자 초대를 요청하세요.',
    NotAuthorizedException: '이메일과 비밀번호를 확인해주세요. 임시 비밀번호의 기한이 지났다면 관리자에게 초대 메일 재발송을 요청하세요.',
    UserNotConfirmedException: '가입 절차가 완료되지 않았습니다. 초대 메일을 확인하거나 관리자에게 문의해주세요.',
    CodeMismatchException: '인증 코드가 일치하지 않습니다. 받은 메일의 코드를 다시 확인해주세요.',
    ExpiredCodeException: '인증 코드가 만료됐습니다. 새 코드를 요청해주세요.',
    InvalidPasswordException: PASSWORD_REQUIREMENTS,
    LimitExceededException: '요청 한도를 초과했습니다. 잠시 후 다시 시도해주세요.',
    TooManyRequestsException: '요청이 많습니다. 잠시 후 다시 시도해주세요.',
    CodeDeliveryFailureException: '인증 메일 발송 요청에 실패했습니다. 관리자에게 문의해주세요.',
  };
  return messages[name] || (error instanceof Error ? error.message : fallback);
}
