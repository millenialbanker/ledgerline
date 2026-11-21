package __PACKAGE__;

// The LedgerlineShell plugin, Android (see MainActivity.java). The device
// secret is encrypted with an AES-256-GCM key in the Android Keystore that
// can be used only after a strong biometric check (BiometricPrompt with a
// CryptoObject); enrolling a new fingerprint invalidates the key, and the
// app then asks for the password. Requires androidx.biometric (added to
// app/build.gradle by native.mjs).

import android.content.Context;
import android.content.SharedPreferences;
import android.content.pm.PackageManager;
import android.net.Uri;
import android.os.Build;
import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyPermanentlyInvalidatedException;
import android.security.keystore.KeyProperties;
import android.util.Base64;

import androidx.annotation.NonNull;
import androidx.biometric.BiometricManager;
import androidx.biometric.BiometricPrompt;
import androidx.core.content.ContextCompat;
import androidx.fragment.app.FragmentActivity;

import com.getcapacitor.JSArray;
import com.getcapacitor.JSObject;
import com.getcapacitor.Plugin;
import com.getcapacitor.PluginCall;
import com.getcapacitor.PluginMethod;
import com.getcapacitor.annotation.CapacitorPlugin;

import java.io.File;
import java.nio.charset.StandardCharsets;
import java.security.KeyStore;

import javax.crypto.Cipher;
import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;
import javax.crypto.spec.GCMParameterSpec;

@CapacitorPlugin(name = "LedgerlineShell")
public class LedgerlineShellPlugin extends Plugin {

    static final String APP_PREFS = "ledgerline.app";
    private static final String VAULT_PREFS = "ledgerline.vault";
    private static final String KEYSTORE = "AndroidKeyStore";
    private static final int STRONG = BiometricManager.Authenticators.BIOMETRIC_STRONG;

    static boolean hostAllowed(String host) {
        if (host == null) return false;
        String h = host.toLowerCase();
        for (String p : MainActivity.ALLOWED_HOSTS) {
            String q = p.toLowerCase();
            if (q.equals("*") || q.equals(h) || (q.startsWith("*.") && h.endsWith(q.substring(1)))) return true;
        }
        return false;
    }

    @PluginMethod
    public void biometry(PluginCall call) {
        int s = BiometricManager.from(getContext()).canAuthenticate(STRONG);
        PackageManager pm = getContext().getPackageManager();
        boolean face = Build.VERSION.SDK_INT >= 29 && pm.hasSystemFeature(PackageManager.FEATURE_FACE) && !pm.hasSystemFeature(PackageManager.FEATURE_FINGERPRINT);
        JSObject r = new JSObject();
        r.put("available", s == BiometricManager.BIOMETRIC_SUCCESS);
        r.put("kind", face ? "face" : "fingerprint");
        call.resolve(r);
    }

    private static String alias(String key) { return "ll." + Integer.toHexString(key.hashCode()); }

    private SecretKey newKey(String alias) throws Exception {
        KeyStore ks = KeyStore.getInstance(KEYSTORE);
        ks.load(null);
        if (ks.containsAlias(alias)) ks.deleteEntry(alias);
        KeyGenParameterSpec.Builder b = new KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_ENCRYPT | KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .setUserAuthenticationRequired(true)
                .setInvalidatedByBiometricEnrollment(true);
        if (Build.VERSION.SDK_INT >= 30) b.setUserAuthenticationParameters(0, KeyProperties.AUTH_BIOMETRIC_STRONG);
        KeyGenerator g = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, KEYSTORE);
        g.init(b.build());
        return g.generateKey();
    }

    private SecretKey oldKey(String alias) throws Exception {
        KeyStore ks = KeyStore.getInstance(KEYSTORE);
        ks.load(null);
        return (SecretKey) ks.getKey(alias, null);
    }

    private interface WithCipher { void run(Cipher c) throws Exception; }

    // Shows the biometric prompt for the cipher and runs fn with it.
    private void prompt(PluginCall call, String title, Cipher cipher, WithCipher fn) {
        FragmentActivity a = getActivity();
        a.runOnUiThread(() -> {
            BiometricPrompt p = new BiometricPrompt(a, ContextCompat.getMainExecutor(a), new BiometricPrompt.AuthenticationCallback() {
                @Override
                public void onAuthenticationSucceeded(@NonNull BiometricPrompt.AuthenticationResult result) {
                    try {
                        fn.run(result.getCryptoObject().getCipher());
                    } catch (Exception e) {
                        call.reject("Could not use the phone's key", "CRYPTO", e);
                    }
                }

                @Override
                public void onAuthenticationError(int code, @NonNull CharSequence msg) {
                    call.reject(msg.toString(), "CANCELLED");
                }
            });
            BiometricPrompt.PromptInfo info = new BiometricPrompt.PromptInfo.Builder()
                    .setTitle(title)
                    .setNegativeButtonText("Cancel")
                    .setAllowedAuthenticators(STRONG)
                    .build();
            p.authenticate(info, new BiometricPrompt.CryptoObject(cipher));
        });
    }

    @PluginMethod
    public void saveSecret(PluginCall call) {
        String key = call.getString("key"), value = call.getString("value");
        if (key == null || value == null) { call.reject("key and value are needed"); return; }
        try {
            Cipher c = Cipher.getInstance("AES/GCM/NoPadding");
            c.init(Cipher.ENCRYPT_MODE, newKey(alias(key)));
            prompt(call, "Turn on fingerprint unlock", c, done -> {
                byte[] ct = done.doFinal(value.getBytes(StandardCharsets.UTF_8));
                String v = Base64.encodeToString(done.getIV(), Base64.NO_WRAP) + ":" + Base64.encodeToString(ct, Base64.NO_WRAP);
                vault().edit().putString(alias(key), v).apply();
                call.resolve();
            });
        } catch (Exception e) {
            call.reject("This phone cannot keep a secret behind biometrics", "UNAVAILABLE", e);
        }
    }

    @PluginMethod
    public void readSecret(PluginCall call) {
        String key = call.getString("key");
        String v = key == null ? null : vault().getString(alias(key), null);
        if (v == null) { call.reject("No secret on this phone", "NOT_FOUND"); return; }
        String[] parts = v.split(":");
        try {
            Cipher c = Cipher.getInstance("AES/GCM/NoPadding");
            c.init(Cipher.DECRYPT_MODE, oldKey(alias(key)), new GCMParameterSpec(128, Base64.decode(parts[0], Base64.NO_WRAP)));
            prompt(call, call.getString("reason", "Unlock"), c, done -> {
                byte[] pt = done.doFinal(Base64.decode(parts[1], Base64.NO_WRAP));
                JSObject r = new JSObject();
                r.put("value", new String(pt, StandardCharsets.UTF_8));
                call.resolve(r);
            });
        } catch (KeyPermanentlyInvalidatedException e) {
            // A fingerprint was added or removed: the secret is gone.
            vault().edit().remove(alias(key)).apply();
            call.reject("Your fingerprints changed; sign in with your password", "NOT_FOUND");
        } catch (Exception e) {
            call.reject("Could not use the phone's key", "CRYPTO", e);
        }
    }

    @PluginMethod
    public void deleteSecret(PluginCall call) {
        String key = call.getString("key");
        if (key != null) {
            vault().edit().remove(alias(key)).apply();
            try {
                KeyStore ks = KeyStore.getInstance(KEYSTORE);
                ks.load(null);
                ks.deleteEntry(alias(key));
            } catch (Exception ignored) {
            }
        }
        call.resolve();
    }

    private SharedPreferences vault() { return getContext().getSharedPreferences(VAULT_PREFS, Context.MODE_PRIVATE); }

    // Signs of a rooted phone. The app warns; it does not refuse.
    @PluginMethod
    public void integrity(PluginCall call) {
        JSArray reasons = new JSArray();
        String tags = Build.TAGS;
        if (tags != null && tags.contains("test-keys")) reasons.put("test-keys build");
        for (String p : new String[] { "/system/app/Superuser.apk", "/sbin/su", "/system/bin/su", "/system/xbin/su", "/data/local/xbin/su", "/data/local/bin/su", "/system/sd/xbin/su", "/system/bin/failsafe/su", "/data/local/su", "/su/bin/su", "/data/adb/magisk" }) {
            if (new File(p).exists()) reasons.put(p);
        }
        JSObject r = new JSObject();
        r.put("rooted", reasons.length() > 0);
        r.put("reasons", reasons);
        call.resolve(r);
    }

    @PluginMethod
    public void getServer(PluginCall call) {
        SharedPreferences p = getContext().getSharedPreferences(APP_PREFS, Context.MODE_PRIVATE);
        JSObject r = new JSObject();
        r.put("url", p.getString("server", ""));
        r.put("firm", p.getString("firm", ""));
        r.put("runtime", MainActivity.RUNTIME_SERVER);
        call.resolve(r);
    }

    // The staff app's server: https, and a host the build allows.
    @PluginMethod
    public void setServer(PluginCall call) {
        if (!MainActivity.RUNTIME_SERVER) { call.reject("This app's server is fixed"); return; }
        Uri u = Uri.parse(call.getString("url", ""));
        if (!"https".equals(u.getScheme()) || !hostAllowed(u.getHost())) { call.reject("This app cannot connect to that server", "NOT_ALLOWED"); return; }
        String root = "https://" + u.getHost() + (u.getPort() > 0 ? ":" + u.getPort() : "");
        getContext().getSharedPreferences(APP_PREFS, Context.MODE_PRIVATE).edit().putString("server", root).putString("firm", call.getString("firm", "")).apply();
        call.resolve();
        getActivity().runOnUiThread(() -> getActivity().recreate());
    }

    @PluginMethod
    public void resetServer(PluginCall call) {
        getContext().getSharedPreferences(APP_PREFS, Context.MODE_PRIVATE).edit().clear().apply();
        call.resolve();
        getActivity().runOnUiThread(() -> getActivity().recreate());
    }
}
