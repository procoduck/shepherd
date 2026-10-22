/**
 * Shared state for the admin → users forms (S9b), split out of
 * AdminUsersPage.tsx so CreateUserModal/EditUserModal/ResetPasswordModal can
 * share it without importing from the page module.
 */

export interface CreateUserFormState {
  login: string;
  email: string;
  displayName: string;
  password: string;
  isAppAdmin: boolean;
  mustChangePassword: boolean;
}

export const emptyCreateUserForm: CreateUserFormState = {
  login: '',
  email: '',
  displayName: '',
  password: '',
  isAppAdmin: false,
  mustChangePassword: true,
};
