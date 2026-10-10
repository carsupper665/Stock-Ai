import * as oidc from './providers/oidc.js'

export async function login(input) {
  await oidc.login(input)
}
