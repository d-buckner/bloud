export const TEST_CREDS = {
  USERNAME: process.env.BLOUD_E2E_USERNAME ?? 'admin',
  PASSWORD: process.env.BLOUD_E2E_PASSWORD ?? 'password',
} as const;
