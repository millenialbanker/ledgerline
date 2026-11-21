package __PACKAGE__;

// Ledgerline mobile shell, Android. Written into the app's package by
// mobile/shared/native.mjs; edit the copy in mobile/shared/android and run
// `npm run native` again. Not compiled in the repository's CI: build it in
// Android Studio (see the app's README). Written for Capacitor 7.

import android.app.AlertDialog;
import android.content.SharedPreferences;
import android.net.Uri;
import android.os.Bundle;
import android.util.Base64;
import android.view.WindowManager;

import com.getcapacitor.BridgeActivity;
import com.getcapacitor.CapConfig;

import java.net.URL;
import java.security.MessageDigest;
import java.security.cert.Certificate;
import java.util.Arrays;
import java.util.List;

import javax.net.ssl.HttpsURLConnection;

public class MainActivity extends BridgeActivity {

    // Filled in by native.mjs from app.config.json or brand.json.
    static final boolean RUNTIME_SERVER = __RUNTIME_SERVER__;
    static final List<String> ALLOWED_HOSTS = Arrays.asList(__ALLOWED_HOSTS__);
    static final List<String> PINS = Arrays.asList(__PINS__);
    static final String PIN_HOST = __PIN_HOST__;
    static final String APP_PATH = __APP_PATH__;

    @Override
    public void onCreate(Bundle savedInstanceState) {
        registerPlugin(LedgerlineShellPlugin.class);
        // The staff app opens the server and firm saved from the set-up QR
        // code; before that, the bundled set-up page (www/index.html).
        if (RUNTIME_SERVER) {
            SharedPreferences p = getSharedPreferences(LedgerlineShellPlugin.APP_PREFS, MODE_PRIVATE);
            String server = p.getString("server", null);
            String firm = p.getString("firm", "");
            if (server != null && LedgerlineShellPlugin.hostAllowed(Uri.parse(server).getHost())) {
                config = new CapConfig.Builder(this)
                        .setServerUrl(server + APP_PATH + "?firm=" + Uri.encode(firm))
                        .setAllowNavigation(new String[] { Uri.parse(server).getHost() })
                        .setWebContentsDebuggingEnabled(false)
                        .create();
            }
        }
        super.onCreate(savedInstanceState);
        // No screenshots, screen recording or recents thumbnail of any
        // screen: every screen of the app shows account data.
        getWindow().setFlags(WindowManager.LayoutParams.FLAG_SECURE, WindowManager.LayoutParams.FLAG_SECURE);
        if (!PINS.isEmpty() && !PIN_HOST.isEmpty()) checkPin();
    }

    // Optional certificate pinning (brand.json "pinning"): the server's
    // public key must be one of the pins, or the app stops. Android's
    // WebView does not apply network_security_config pins to page loads,
    // so the app checks the key itself when it starts.
    private void checkPin() {
        new Thread(() -> {
            boolean ok = false;
            try {
                HttpsURLConnection c = (HttpsURLConnection) new URL("https://" + PIN_HOST + "/healthz").openConnection();
                c.setConnectTimeout(10000);
                c.setReadTimeout(10000);
                c.connect();
                Certificate[] certs = c.getServerCertificates();
                byte[] spki = certs[0].getPublicKey().getEncoded();
                String pin = Base64.encodeToString(MessageDigest.getInstance("SHA-256").digest(spki), Base64.NO_WRAP);
                ok = PINS.contains(pin);
                c.disconnect();
            } catch (Exception e) {
                ok = true; // offline: the page itself fails to load; nothing to compare
            }
            if (!ok) {
                runOnUiThread(() -> {
                    if (bridge != null) bridge.getWebView().loadUrl("about:blank");
                    new AlertDialog.Builder(this)
                            .setTitle("Cannot verify the server")
                            .setMessage("This network may be intercepting the connection. The app will close. Try again on another network, or update the app.")
                            .setCancelable(false)
                            .setPositiveButton("Close", (d, w) -> finish())
                            .show();
                });
            }
        }).start();
    }
}
