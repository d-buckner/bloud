export const TEST_CREDS = {
  USERNAME: process.env.BLOUD_E2E_USERNAME ?? 'e2etest',
  PASSWORD: process.env.BLOUD_E2E_PASSWORD ?? 'e2etest123',
} as const;
