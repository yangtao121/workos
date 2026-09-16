import importlib.util
import json
import pathlib
import tempfile
import unittest
spec=importlib.util.spec_from_file_location('budget_proxy',pathlib.Path(__file__).with_name('budget_proxy.py'))
proxy=importlib.util.module_from_spec(spec)
spec.loader.exec_module(proxy)

class BudgetTest(unittest.TestCase):
    def test_reservations_survive_failures_and_restart(self):
        with tempfile.TemporaryDirectory() as directory:
            ledger=pathlib.Path(directory)/'ledger.jsonl'
            budget=proxy.Budget(ledger)
            body=json.dumps({'model':'deepseek-v4-flash','max_tokens':8192,'messages':[{'role':'user','content':'small fixture'}]}).encode()
            count=0
            while True:
                try: budget.reserve(body); count+=1
                except ValueError: break
            self.assertGreater(count,0)
            self.assertLessEqual(budget.spent,1_900_000)
            restored=proxy.Budget(ledger)
            self.assertEqual(restored.spent,budget.spent)
            with self.assertRaises(ValueError): restored.reserve(body)
            self.assertNotIn('small fixture',ledger.read_text())

    def test_unpriced_and_unbounded_requests_never_reserve(self):
        with tempfile.TemporaryDirectory() as directory:
            budget=proxy.Budget(pathlib.Path(directory)/'ledger.jsonl')
            for value in [{'model':'deepseek-pro','max_tokens':1},{'model':'deepseek-v4-flash'},{'model':'deepseek-v4-flash','max_tokens':8193},{'model':'deepseek-v4-flash','max_tokens':True}]:
                with self.assertRaises(ValueError): budget.reserve(json.dumps(value).encode())
            self.assertEqual(budget.spent,0)
if __name__=='__main__':unittest.main()
