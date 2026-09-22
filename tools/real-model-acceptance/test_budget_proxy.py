import concurrent.futures
import json
import pathlib
import tempfile
import unittest
from budget_proxy import Budget, LIMIT_MICROCNY

class BudgetTest(unittest.TestCase):
    def test_concurrent_reservations_restart_and_exclusive_ledger(self):
        with tempfile.TemporaryDirectory() as directory:
            ledger=pathlib.Path(directory)/'usage.jsonl'
            budget=Budget(ledger)
            body=json.dumps({'model':'deepseek-v4-flash','max_tokens':8192,'messages':[{'role':'user','content':'private-fixture-text'}]}).encode()
            def reserve(_):
                try:budget.reserve(body);return True
                except ValueError:return False
            with concurrent.futures.ThreadPoolExecutor(max_workers=12) as executor:
                admitted=sum(executor.map(reserve,range(400)))
            self.assertGreater(admitted,0)
            self.assertLess(admitted,400)
            records=[json.loads(line) for line in ledger.read_text().splitlines()]
            total=sum(record['reserved_microcny'] for record in records)
            self.assertEqual(total,budget.spent)
            self.assertNotIn("private-fixture-text",ledger.read_text())
            self.assertLessEqual(total,LIMIT_MICROCNY)
            self.assertEqual(ledger.stat().st_mode & 0o777,0o600)
            with self.assertRaises(BlockingIOError):Budget(ledger)
            budget.process_lock.close()
            restarted=Budget(ledger)
            self.assertEqual(restarted.spent,total)
            with self.assertRaises(ValueError):restarted.reserve(body)
            restarted.process_lock.close()
    def test_unpriced_or_unbounded_requests_never_consume(self):
        with tempfile.TemporaryDirectory() as directory:
            budget=Budget(pathlib.Path(directory)/'usage.jsonl')
            for request in [{'model':'unknown','max_tokens':10},{'model':'deepseek-v4-flash'},{'model':'deepseek-v4-flash','max_tokens':True},{'model':'deepseek-v4-flash','max_tokens':8193}]:
                with self.assertRaises(ValueError):budget.reserve(json.dumps(request).encode())
            self.assertEqual(budget.spent,0)
            budget.process_lock.close()
if __name__=='__main__':unittest.main()
