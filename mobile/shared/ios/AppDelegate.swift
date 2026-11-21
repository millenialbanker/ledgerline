// Ledgerline mobile shell, iOS. Written into ios/App/App/AppDelegate.swift
// by mobile/shared/native.mjs (so the Xcode project needs no new files);
// edit the copy in mobile/shared/ios and run `npm run native` again.
//
// Not compiled in the repository's CI: build it on a Mac (see the app's
// README). Written for Capacitor 7 and Firebase iOS SDK 11.
//
//   AppDelegate            push (APNs token -> FCM token -> Capacitor's push
//                          plugin), links, and the cover that hides the
//                          screen in the app switcher and while it is
//                          recorded or mirrored
//   MainViewController     registers the LedgerlineShell plugin; in the
//                          staff app, opens the firm's server set up from
//                          the QR code
//   LedgerlineShellPlugin  the device secret in the keychain behind Face ID
//                          or Touch ID, the jailbreak check, the staff
//                          app's server address
//
// iOS has no way to block screenshots. The app hides its screen whenever
// it leaves the foreground and while the screen is captured, and the pages
// show nothing that a push could leak.

import UIKit
import Capacitor
import FirebaseCore
import FirebaseMessaging
import LocalAuthentication
import Security

// Filled in by native.mjs from app.config.json or brand.json.
let llRuntimeServer = __RUNTIME_SERVER__
let llAllowedHosts: [String] = __ALLOWED_HOSTS__
let llAppPath = __APP_PATH__

@main
class AppDelegate: UIResponder, UIApplicationDelegate {

    var window: UIWindow?
    private var cover: UIView?

    func application(_ application: UIApplication, didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]?) -> Bool {
        // Push works once GoogleService-Info.plist is in the app (README).
        if Bundle.main.path(forResource: "GoogleService-Info", ofType: "plist") != nil {
            FirebaseApp.configure()
        }
        NotificationCenter.default.addObserver(self, selector: #selector(captureChanged), name: UIScreen.capturedDidChangeNotification, object: nil)
        return true
    }

    func applicationWillResignActive(_ application: UIApplication) { showCover() }
    func applicationDidEnterBackground(_ application: UIApplication) { showCover() }
    func applicationDidBecomeActive(_ application: UIApplication) {
        if !UIScreen.main.isCaptured { hideCover() }
    }

    @objc private func captureChanged() {
        if UIScreen.main.isCaptured { showCover() } else if UIApplication.shared.applicationState == .active { hideCover() }
    }

    private func showCover() {
        guard cover == nil, let w = window else { return }
        let v = UIVisualEffectView(effect: UIBlurEffect(style: .systemMaterial))
        v.frame = w.bounds
        v.autoresizingMask = [.flexibleWidth, .flexibleHeight]
        let label = UILabel()
        label.text = Bundle.main.object(forInfoDictionaryKey: "CFBundleDisplayName") as? String ?? ""
        label.font = .preferredFont(forTextStyle: .headline)
        label.textAlignment = .center
        label.frame = v.bounds
        label.autoresizingMask = [.flexibleWidth, .flexibleHeight]
        v.contentView.addSubview(label)
        w.addSubview(v)
        cover = v
    }

    private func hideCover() {
        cover?.removeFromSuperview()
        cover = nil
    }

    func application(_ app: UIApplication, open url: URL, options: [UIApplication.OpenURLOptionsKey: Any] = [:]) -> Bool {
        return ApplicationDelegateProxy.shared.application(app, open: url, options: options)
    }

    func application(_ application: UIApplication, continue userActivity: NSUserActivity, restorationHandler: @escaping ([UIUserActivityRestoring]?) -> Void) -> Bool {
        return ApplicationDelegateProxy.shared.application(application, continue: userActivity, restorationHandler: restorationHandler)
    }

    // FCM HTTP v1 addresses phones by FCM token, so the APNs token is
    // handed to Firebase and the FCM token to Capacitor's push plugin.
    func application(_ application: UIApplication, didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
        guard FirebaseApp.app() != nil else {
            NotificationCenter.default.post(name: .capacitorDidFailToRegisterForRemoteNotifications, object: NSError(domain: "Ledgerline", code: 1, userInfo: [NSLocalizedDescriptionKey: "Firebase is not set up in this build"]))
            return
        }
        Messaging.messaging().apnsToken = deviceToken
        Messaging.messaging().token { token, error in
            if let error = error {
                NotificationCenter.default.post(name: .capacitorDidFailToRegisterForRemoteNotifications, object: error)
            } else if let token = token {
                NotificationCenter.default.post(name: .capacitorDidRegisterForRemoteNotifications, object: token)
            }
        }
    }

    func application(_ application: UIApplication, didFailToRegisterForRemoteNotificationsWithError error: Error) {
        NotificationCenter.default.post(name: .capacitorDidFailToRegisterForRemoteNotifications, object: error)
    }
}

// The storyboard's view controller (native.mjs sets Main.storyboard to it).
class MainViewController: CAPBridgeViewController {

    override open func capacitorDidLoad() {
        bridge?.registerPluginInstance(LedgerlineShellPlugin())
    }

    // The staff app opens the server and firm saved from the set-up QR
    // code; before that, the bundled set-up page (www/index.html).
    override open func instanceDescriptor() -> InstanceDescriptor {
        let d = super.instanceDescriptor()
        if llRuntimeServer, let s = UserDefaults.standard.string(forKey: "ll.server"), let u = URL(string: s), let host = u.host, hostAllowed(host) {
            let firm = UserDefaults.standard.string(forKey: "ll.firm") ?? ""
            d.serverURL = s + llAppPath + "?firm=" + (firm.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? "")
            d.allowedNavigationHostnames.append(host)
        }
        return d
    }
}

func hostAllowed(_ host: String) -> Bool {
    let h = host.lowercased()
    return llAllowedHosts.contains { p in
        let p = p.lowercased()
        return p == "*" || p == h || (p.hasPrefix("*.") && h.hasSuffix(String(p.dropFirst())))
    }
}

@objc(LedgerlineShellPlugin)
public class LedgerlineShellPlugin: CAPPlugin, CAPBridgedPlugin {
    public let identifier = "LedgerlineShellPlugin"
    public let jsName = "LedgerlineShell"
    public let pluginMethods: [CAPPluginMethod] = [
        CAPPluginMethod(name: "biometry", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "saveSecret", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "readSecret", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "deleteSecret", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "integrity", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "getServer", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "setServer", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "resetServer", returnType: CAPPluginReturnPromise),
    ]
    private let service = (Bundle.main.bundleIdentifier ?? "ledgerline") + ".vault"

    @objc func biometry(_ call: CAPPluginCall) {
        let c = LAContext()
        var err: NSError?
        let ok = c.canEvaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, error: &err)
        call.resolve(["available": ok, "kind": c.biometryType == .faceID ? "face" : (c.biometryType == .touchID ? "fingerprint" : "none")])
    }

    private func base(_ key: String) -> [String: Any] {
        return [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service, kSecAttrAccount as String: key]
    }

    // The secret is readable only on this phone, only while it has a
    // passcode, and only after Face ID or Touch ID of the biometrics
    // enrolled now (adding a fingerprint makes it unreadable).
    @objc func saveSecret(_ call: CAPPluginCall) {
        guard let key = call.getString("key"), let value = call.getString("value") else { call.reject("key and value are needed"); return }
        var cfErr: Unmanaged<CFError>?
        guard let access = SecAccessControlCreateWithFlags(nil, kSecAttrAccessibleWhenPasscodeSetThisDeviceOnly, .biometryCurrentSet, &cfErr) else {
            call.reject("This phone cannot keep a secret behind Face ID or Touch ID"); return
        }
        SecItemDelete(base(key) as CFDictionary)
        var q = base(key)
        q[kSecValueData as String] = Data(value.utf8)
        q[kSecAttrAccessControl as String] = access
        let st = SecItemAdd(q as CFDictionary, nil)
        if st == errSecSuccess { call.resolve() } else { call.reject("Could not save to the keychain", String(st)) }
    }

    @objc func readSecret(_ call: CAPPluginCall) {
        guard let key = call.getString("key") else { call.reject("key is needed"); return }
        let ctx = LAContext()
        ctx.localizedReason = call.getString("reason") ?? "Unlock"
        var q = base(key)
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        q[kSecUseAuthenticationContext as String] = ctx
        DispatchQueue.global(qos: .userInitiated).async {
            var out: CFTypeRef?
            let st = SecItemCopyMatching(q as CFDictionary, &out)
            if st == errSecSuccess, let d = out as? Data, let v = String(data: d, encoding: .utf8) {
                call.resolve(["value": v])
            } else if st == errSecItemNotFound {
                call.reject("No secret on this phone", "NOT_FOUND")
            } else {
                call.reject("Not unlocked", String(st))
            }
        }
    }

    @objc func deleteSecret(_ call: CAPPluginCall) {
        if let key = call.getString("key") { SecItemDelete(base(key) as CFDictionary) }
        call.resolve()
    }

    // Signs of a jailbreak. The app warns; it does not refuse.
    @objc func integrity(_ call: CAPPluginCall) {
        var reasons: [String] = []
        #if !targetEnvironment(simulator)
        for p in ["/Applications/Cydia.app", "/Applications/Sileo.app", "/Library/MobileSubstrate/MobileSubstrate.dylib", "/bin/bash", "/usr/sbin/sshd", "/etc/apt", "/private/var/lib/apt/", "/var/jb"] where FileManager.default.fileExists(atPath: p) {
            reasons.append(p)
        }
        let probe = "/private/ll-jb-probe.txt"
        if (try? "x".write(toFile: probe, atomically: true, encoding: .utf8)) != nil {
            reasons.append("writable outside the app")
            try? FileManager.default.removeItem(atPath: probe)
        }
        #endif
        call.resolve(["rooted": !reasons.isEmpty, "reasons": reasons])
    }

    @objc func getServer(_ call: CAPPluginCall) {
        let d = UserDefaults.standard
        call.resolve(["url": d.string(forKey: "ll.server") ?? "", "firm": d.string(forKey: "ll.firm") ?? "", "runtime": llRuntimeServer])
    }

    // The staff app's server: https, and a host the build allows.
    @objc func setServer(_ call: CAPPluginCall) {
        guard llRuntimeServer else { call.reject("This app's server is fixed"); return }
        guard let s = call.getString("url"), let u = URL(string: s), u.scheme == "https", let host = u.host, hostAllowed(host) else {
            call.reject("This app cannot connect to that server", "NOT_ALLOWED"); return
        }
        var root = "https://" + host
        if let p = u.port { root += ":\(p)" }
        UserDefaults.standard.set(root, forKey: "ll.server")
        UserDefaults.standard.set(call.getString("firm") ?? "", forKey: "ll.firm")
        call.resolve()
        restart()
    }

    @objc func resetServer(_ call: CAPPluginCall) {
        UserDefaults.standard.removeObject(forKey: "ll.server")
        UserDefaults.standard.removeObject(forKey: "ll.firm")
        call.resolve()
        restart()
    }

    private func restart() {
        DispatchQueue.main.async {
            guard let w = UIApplication.shared.delegate?.window ?? nil else { return }
            w.rootViewController = MainViewController()
            w.makeKeyAndVisible()
        }
    }
}
