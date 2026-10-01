export function localTestURL() {
  const address = process.env.DUPABASE_TEST_URL;
  if (!address) throw new Error('Set DUPABASE_TEST_URL to an explicitly disposable local instance');
  if (!['127.0.0.1', 'localhost', '[::1]'].includes(new URL(address).hostname)) {
    throw new Error('Remote destructive compatibility test targets are forbidden');
  }
  return address.replace(/\/$/, '');
}
