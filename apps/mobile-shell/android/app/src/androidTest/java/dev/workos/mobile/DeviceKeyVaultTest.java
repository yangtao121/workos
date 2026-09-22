package dev.workos.mobile;

import static org.junit.Assert.*;
import android.content.Context;
import android.content.SharedPreferences;
import androidx.test.ext.junit.runners.AndroidJUnit4;
import androidx.test.platform.app.InstrumentationRegistry;
import java.security.GeneralSecurityException;
import java.security.KeyStore;
import org.junit.After;
import org.junit.Before;
import org.junit.Test;
import org.junit.runner.RunWith;

@RunWith(AndroidJUnit4.class)
public class DeviceKeyVaultTest {
    private Context context;
    private String alias;
    private SharedPreferences preferences;
    private DeviceKeyVault vault;

    @Before
    public void prepare() throws Exception {
        context = InstrumentationRegistry.getInstrumentation().getTargetContext();
        alias = context.getPackageName() + ".test-vault";
        preferences = context.getSharedPreferences("workos_test_vault", Context.MODE_PRIVATE);
        clean();
        vault = new DeviceKeyVault(context, "workos_test_vault", alias);
    }

    @After
    public void clean() throws Exception {
        assertTrue(preferences.edit().clear().commit());
        KeyStore store = KeyStore.getInstance("AndroidKeyStore");
        store.load(null);
        store.deleteEntry(alias);
    }

    @Test
    public void ciphertextUsesNonExportableKeystoreAndSurvivesRecreation() throws Exception {
        String fixture = "fixture-only-private-identity";
        vault.set("server-a", fixture);
        String first = preferences.getString("server-a", "");
        assertTrue(first.startsWith("v1."));
        assertFalse(first.contains(fixture));
        vault.set("server-a", fixture);
        assertNotEquals(first, preferences.getString("server-a", ""));
        DeviceKeyVault recreated = new DeviceKeyVault(context, "workos_test_vault", alias);
        assertEquals(fixture, recreated.get("server-a"));
        KeyStore store = KeyStore.getInstance("AndroidKeyStore");
        store.load(null);
        assertNull("Keystore encryption key must not be exportable", store.getKey(alias, null).getEncoded());
        recreated.remove("server-a");
        recreated.remove("server-a");
        assertNull(recreated.get("server-a"));
    }

    @Test
    public void corruptionCrossOriginSwapAndLostKeystoreCannotBecomeMissingIdentity() throws Exception {
        vault.set("server-a", "fixture-only-identity");
        String encrypted = preferences.getString("server-a", "");
        assertTrue(preferences.edit().putString("server-b", encrypted).commit());
        assertThrows(GeneralSecurityException.class, () -> vault.get("server-b"));
        assertTrue(preferences.edit().putString("server-a", "ordinary-base64-is-not-encryption").commit());
        assertThrows(GeneralSecurityException.class, () -> vault.get("server-a"));
        assertTrue(preferences.edit().putString("server-a", encrypted).commit());
        KeyStore store = KeyStore.getInstance("AndroidKeyStore");
        store.load(null);
        store.deleteEntry(alias);
        assertThrows(GeneralSecurityException.class, () -> vault.get("server-a"));
        assertThrows(GeneralSecurityException.class, () -> vault.checkAvailable());
        assertFalse(store.containsAlias(alias));
    }
}
